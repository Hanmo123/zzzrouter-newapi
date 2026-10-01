/*
Copyright (C) 2023-2026 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as
published by the Free Software Foundation, either version 3 of the
License, or (at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program. If not, see <https://www.gnu.org/licenses/>.

For commercial licensing, please contact support@quantumnous.com
*/
import assert from 'node:assert/strict'
import { createHash } from 'node:crypto'
import { mkdtemp, rm, writeFile } from 'node:fs/promises'
import { createServer, request } from 'node:http'
import { tmpdir } from 'node:os'
import path from 'node:path'
import { after, before, test } from 'node:test'
import { fileURLToPath } from 'node:url'

import { createRsbuild, loadConfig } from '@rsbuild/core'

const root = fileURLToPath(new URL('../../', import.meta.url))
let frontend
let directory
let origin
let stream
let websocketRequest
const backend = createServer(async (req, res) => {
  if (req.url === '/v1/stream') {
    stream = res
    res.writeHead(200, { 'Content-Type': 'text/event-stream' })
    res.write('data: first-event\n\n')
    return
  }
  const chunks = []
  for await (const chunk of req) chunks.push(chunk)
  res.writeHead(req.url === '/api/missing' ? 404 : 200, {
    'Content-Type': 'application/json',
  })
  res.end(
    JSON.stringify({
      url: req.url,
      method: req.method,
      body: Buffer.concat(chunks).toString(),
      cookie: req.headers.cookie,
      authorization: req.headers.authorization,
      origin: req.headers.origin,
    })
  )
})
backend.on('upgrade', (req, socket) => {
  websocketRequest = req.url
  const accept = createHash('sha1')
    .update(
      `${req.headers['sec-websocket-key']}258EAFA5-E914-47DA-95CA-C5AB0DC85B11`
    )
    .digest('base64')
  socket.end(
    `HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: ${accept}\r\n\r\n`
  )
})

before(async () => {
  await new Promise((resolve) => backend.listen(0, '127.0.0.1', resolve))
  const previous = process.env.API_PROXY_TARGET
  process.env.API_PROXY_TARGET = `http://127.0.0.1:${backend.address().port}`
  let config
  try {
    config = (await loadConfig({ cwd: root, envMode: 'development' })).content
  } finally {
    if (previous === undefined) delete process.env.API_PROXY_TARGET
    else process.env.API_PROXY_TARGET = previous
  }
  directory = await mkdtemp(path.join(tmpdir(), 'new-api-proxy-test-'))
  await writeFile(
    path.join(directory, 'index.html'),
    '<!doctype html><title>frontend fixture</title>'
  )
  const rsbuild = await createRsbuild({
    cwd: root,
    rsbuildConfig: {
      logLevel: 'error',
      output: { distPath: { root: directory } },
      server: {
        host: '127.0.0.1',
        port: 0,
        strictPort: true,
        printUrls: false,
        publicDir: false,
        proxy: config.server.proxy,
      },
    },
  })
  frontend = (await rsbuild.preview()).server
  origin = `http://127.0.0.1:${frontend.httpServer.address().port}`
})

after(async () => {
  stream?.end()
  await frontend?.close()
  backend.closeAllConnections()
  await new Promise((resolve) => backend.close(resolve))
  if (directory) await rm(directory, { recursive: true, force: true })
})

test('standalone frontend defaults to port 3001 with the backend proxy on port 3002', async () => {
  const keys = ['API_PROXY_TARGET', 'VITE_REACT_APP_SERVER_URL']
  const previous = new Map(keys.map((key) => [key, process.env[key]]))
  for (const key of keys) delete process.env[key]
  try {
    const config = (await loadConfig({ cwd: root, envMode: 'development' }))
      .content
    assert.equal(config.server.port, 3001)
    assert.equal(config.server.proxy[0].target, 'http://127.0.0.1:3002')
  } finally {
    for (const [key, value] of previous) {
      if (value === undefined) delete process.env[key]
      else process.env[key] = value
    }
  }
})

test('API requests preserve method, path, query, body, authorization, cookie and Origin', async () => {
  const response = await fetch(`${origin}/api/example?q=one`, {
    method: 'POST',
    body: '{"draft":true}',
    headers: {
      Cookie: 'test-cookie=canary',
      Authorization: 'Bearer test-canary',
      Origin: origin,
    },
  })
  assert.equal(response.status, 200)
  assert.deepEqual(await response.json(), {
    url: '/api/example?q=one',
    method: 'POST',
    body: '{"draft":true}',
    cookie: 'test-cookie=canary',
    authorization: 'Bearer test-canary',
    origin,
  })
})

test('relay namespaces proxy to the backend while SPA and callback routes stay on the frontend', async () => {
  for (const url of [
    '/v1/models',
    '/v1beta/models',
    '/v2/example',
    '/mj/example',
    '/pg/example',
    '/fast/mj/example',
    '/dashboard/billing/usage',
  ]) {
    const response = await fetch(origin + url)
    assert.equal((await response.json()).url, url)
  }
  for (const url of ['/channels', '/oauth/oidc', '/api-settings']) {
    const response = await fetch(origin + url)
    assert.match(await response.text(), /frontend fixture/)
  }
  const missing = await fetch(`${origin}/api/missing`)
  assert.equal(missing.status, 404)
  assert.equal((await missing.json()).url, '/api/missing')
})

test('SSE delivers the first event before the backend closes the stream', async () => {
  const response = await fetch(`${origin}/v1/stream`)
  const reader = response.body.getReader()
  try {
    const first = await reader.read()
    assert.match(new TextDecoder().decode(first.value), /data: first-event/)
    assert.equal(stream.writableEnded, false)
  } finally {
    stream.end()
    await reader.cancel()
  }
})

test('WebSocket upgrade requests reach the backend without rewriting their path', async () => {
  const code = await new Promise((resolve, reject) => {
    const req = request(`${origin}/v1/realtime?model=test`, {
      headers: {
        Connection: 'Upgrade',
        Upgrade: 'websocket',
        'Sec-WebSocket-Version': '13',
        'Sec-WebSocket-Key': 'dGhlIHNhbXBsZSBub25jZQ==',
      },
    })
    req.on('upgrade', (response, socket) => {
      socket.destroy()
      resolve(response.statusCode)
    })
    req.on('response', (response) => {
      response.resume()
      resolve(response.statusCode)
    })
    req.on('error', reject)
    req.end()
  })
  assert.equal(code, 101)
  assert.equal(websocketRequest, '/v1/realtime?model=test')
})
