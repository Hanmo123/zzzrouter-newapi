import assert from 'node:assert/strict'
import { spawnSync } from 'node:child_process'
import {
  chmod,
  mkdtemp,
  mkdir,
  readFile,
  rm,
  writeFile,
} from 'node:fs/promises'
import { tmpdir } from 'node:os'
import path from 'node:path'
import { after, before, test } from 'node:test'
import { fileURLToPath } from 'node:url'

const root = fileURLToPath(new URL('../../', import.meta.url))
let fixture

before(async () => {
  fixture = await mkdtemp(path.join(tmpdir(), 'new-api-commands-'))
  await mkdir(path.join(fixture, 'web'))
  await mkdir(path.join(fixture, 'tools'))
  await writeFile(
    path.join(fixture, 'package.json'),
    await readFile(path.join(root, 'package.json'))
  )
  const backend = path.join(fixture, 'tools', 'go')
  await writeFile(
    backend,
    `#!/usr/bin/env node
console.log('COMMAND_RESULT=' + JSON.stringify({kind:'api',port:process.env.PORT,args:process.argv.slice(2),cwd:process.cwd()}))
`
  )
  await chmod(backend, 0o755)
  await writeFile(
    path.join(fixture, 'web', 'package.json'),
    JSON.stringify({
      scripts: { dev: 'node fixture.mjs', preview: 'node fixture.mjs' },
    })
  )
  await writeFile(
    path.join(fixture, 'web', 'fixture.mjs'),
    `
console.log('COMMAND_RESULT=' + JSON.stringify({kind:'web',target:process.env.API_PROXY_TARGET,args:process.argv.slice(2),cwd:process.cwd()}))
`
  )
})

after(async () => {
  if (fixture) await rm(fixture, { recursive: true, force: true })
})

function runCommand(script, extraEnv = {}) {
  const env = {
    ...process.env,
    PATH: `${path.join(fixture, 'tools')}${path.delimiter}${process.env.PATH}`,
    NO_COLOR: '1',
  }
  for (const key of [
    'DEV_API_PORT',
    'DEV_WEB_PORT',
    'API_PROXY_TARGET',
    'PORT',
  ]) {
    delete env[key]
  }
  Object.assign(env, extraEnv)
  const result = spawnSync('bun', ['--no-env-file', 'run', script], {
    cwd: fixture,
    env,
    encoding: 'utf8',
    timeout: 15000,
  })
  assert.equal(result.status, 0, result.stderr)
  return result.stdout
    .split('\n')
    .filter((line) => line.includes('COMMAND_RESULT='))
    .map((line) =>
      JSON.parse(
        line.slice(line.indexOf('COMMAND_RESULT=') + 'COMMAND_RESULT='.length)
      )
    )
}

test('bun run dev:api starts an API-only backend on 3002 from the repository root', () => {
  assert.deepEqual(runCommand('dev:api'), [
    {
      kind: 'api',
      port: '3002',
      args: ['run', '-tags', 'api_only', '.'],
      cwd: fixture,
    },
  ])
})

test('bun run dev:web starts the frontend on 3001 and proxies to the backend on 3002', () => {
  assert.deepEqual(runCommand('dev:web'), [
    {
      kind: 'web',
      target: 'http://127.0.0.1:3002',
      args: ['--port', '3001'],
      cwd: path.join(fixture, 'web'),
    },
  ])
})

test('bun run dev launches both services and applies custom ports consistently', () => {
  const results = runCommand('dev', {
    DEV_API_PORT: '4102',
    DEV_WEB_PORT: '4101',
  }).sort((a, b) => a.kind.localeCompare(b.kind))
  assert.deepEqual(results, [
    {
      kind: 'api',
      port: '4102',
      args: ['run', '-tags', 'api_only', '.'],
      cwd: fixture,
    },
    {
      kind: 'web',
      target: 'http://127.0.0.1:4102',
      args: ['--port', '4101'],
      cwd: path.join(fixture, 'web'),
    },
  ])
})

test('bun run preview:web accepts an explicit backend proxy target', () => {
  assert.deepEqual(
    runCommand('preview:web', {
      API_PROXY_TARGET: 'http://backend.example:5002',
    }),
    [
      {
        kind: 'web',
        target: 'http://backend.example:5002',
        args: ['--port', '3001'],
        cwd: path.join(fixture, 'web'),
      },
    ]
  )
})
