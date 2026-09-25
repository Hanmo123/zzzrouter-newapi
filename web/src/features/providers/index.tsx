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
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { getRouteApi, Link } from '@tanstack/react-router'
import type { ColumnDef } from '@tanstack/react-table'
import { Plus } from 'lucide-react'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { ConfirmDialog } from '@/components/confirm-dialog'
import { DataTablePage, useDataTable } from '@/components/data-table'
import { ErrorState } from '@/components/error-state'
import { SectionPageLayout } from '@/components/layout'
import { Alert, AlertDescription } from '@/components/ui/alert'
import { Button, buttonVariants } from '@/components/ui/button'
import { useTableUrlState } from '@/hooks/use-table-url-state'
import { toIntlLocale } from '@/i18n/languages'
import {
  hasPermission,
  ADMIN_PERMISSION_RESOURCES,
  ADMIN_PERMISSION_ACTIONS,
} from '@/lib/admin-permissions'
import { formatNumber, formatTimestampToDate } from '@/lib/format'
import { getServerErrorMessage } from '@/lib/server-error-message'
import { useAuthStore } from '@/stores/auth-store'

import { deleteProvider, listProviders, providersQueryKey } from './api'
import { ProviderEditor } from './components/provider-editor'
import type { Provider } from './types'

const route = getRouteApi('/_authenticated/providers/')

export function Providers() {
  const { t, i18n } = useTranslation()
  const locale = toIntlLocale(i18n.resolvedLanguage || i18n.language)
  const client = useQueryClient()
  const user = useAuthStore((state) => state.auth.user)
  const canWrite = hasPermission(
    user,
    ADMIN_PERMISSION_RESOURCES.CHANNEL,
    ADMIN_PERMISSION_ACTIONS.WRITE
  )
  const [editor, setEditor] = useState<Provider | 'new' | null>(null)
  const [deleting, setDeleting] = useState<Provider | null>(null)
  const state = useTableUrlState({
    search: route.useSearch(),
    navigate: route.useNavigate(),
    pagination: { defaultPageSize: 20 },
    globalFilter: { enabled: true, key: 'filter' },
  })
  const params = {
    p: state.pagination.pageIndex + 1,
    page_size: state.pagination.pageSize,
    keyword: state.globalFilter,
  }
  const query = useQuery({
    queryKey: [...providersQueryKey, 'list', params],
    queryFn: ({ signal }) => listProviders(params, signal),
    meta: { errorToast: false },
  })
  const remove = useMutation({
    mutationFn: deleteProvider,
    meta: { errorToast: false },
    onSuccess: async () => {
      await client.invalidateQueries({ queryKey: providersQueryKey })
      setDeleting(null)
      toast.success(t('Provider deleted successfully'))
    },
  })
  const columns: ColumnDef<Provider>[] = [
    {
      accessorKey: 'id',
      header: t('ID'),
      size: 80,
      cell: ({ row }) => formatNumber(row.original.id, locale),
    },
    {
      accessorKey: 'name',
      header: t('Provider Name'),
      size: 220,
      enableHiding: false,
      meta: { mobileTitle: true },
      cell: ({ row }) => (
        <span className='line-clamp-2 break-words' title={row.original.name}>
          {row.original.name}
        </span>
      ),
    },
    {
      accessorKey: 'description',
      header: t('Description'),
      size: 320,
      cell: ({ row }) => (
        <p
          className='text-muted-foreground line-clamp-2 max-w-lg break-words whitespace-normal'
          title={row.original.description}
        >
          {row.original.description || '—'}
        </p>
      ),
    },
    {
      accessorKey: 'channel_count',
      header: t('Linked channels'),
      size: 150,
      cell: ({ row }) => (
        <Link
          to='/channels'
          search={{ provider_id: row.original.id, status: ['all'], page: 1 }}
          aria-label={t('View channels for {{name}}', {
            name: row.original.name,
          })}
          className={buttonVariants({
            variant: 'link',
            className: 'h-auto p-0 tabular-nums',
          })}
        >
          {formatNumber(row.original.channel_count, locale)}
        </Link>
      ),
    },
    {
      accessorKey: 'created_time',
      header: t('Created At'),
      cell: ({ row }) => formatTimestampToDate(row.original.created_time),
    },
    {
      id: 'actions',
      header: t('Actions'),
      enableHiding: false,
      size: 160,
      cell: ({ row }) =>
        canWrite ? (
          <div className='flex gap-1'>
            <Button
              variant='ghost'
              size='sm'
              aria-label={t('Edit {{name}}', { name: row.original.name })}
              onClick={() => setEditor(row.original)}
            >
              {t('Edit')}
            </Button>
            <Button
              variant='ghost'
              size='sm'
              className='text-destructive'
              aria-label={t('Delete {{name}}', { name: row.original.name })}
              onClick={() => {
                remove.reset()
                setDeleting(row.original)
              }}
            >
              {t('Delete')}
            </Button>
          </div>
        ) : (
          '—'
        ),
    },
  ]
  const { table } = useDataTable({
    data: query.data?.items ?? [],
    columns,
    totalCount: query.data?.total ?? 0,
    ...state,
    // Do not reset a bookmarked page before its count has loaded.
    ensurePageInRange: query.isSuccess
      ? (pageCount) => state.ensurePageInRange(Math.max(1, pageCount))
      : undefined,
    manualPagination: true,
    manualFiltering: true,
    enableSorting: false,
    enableRowSelection: false,
    getRowId: (provider) => String(provider.id),
    columnVisibilityStorageKey: false,
    initialColumnVisibility: { created_time: false },
  })

  return (
    <>
      <SectionPageLayout fixedContent>
        <SectionPageLayout.Title>{t('Providers')}</SectionPageLayout.Title>
        <SectionPageLayout.Actions>
          {canWrite && (
            <Button size='sm' onClick={() => setEditor('new')}>
              <Plus className='size-4' />
              {t('Add Provider')}
            </Button>
          )}
        </SectionPageLayout.Actions>
        <SectionPageLayout.Content>
          {query.isError ? (
            <ErrorState
              description={getServerErrorMessage(query.error)}
              onRetry={() => void query.refetch()}
            />
          ) : (
            <DataTablePage
              table={table}
              columns={columns}
              isLoading={query.isLoading}
              isFetching={query.isFetching}
              emptyTitle={t('No providers found')}
              emptyDescription={t('Add a provider or adjust your search.')}
              applyHeaderSize
              toolbarProps={{
                searchPlaceholder: t('Search provider names'),
                searchDebounceMs: 300,
              }}
            />
          )}
        </SectionPageLayout.Content>
      </SectionPageLayout>
      {editor && canWrite && (
        <ProviderEditor
          key={editor === 'new' ? 'new' : editor.id}
          provider={editor === 'new' ? undefined : editor}
          onClose={() => setEditor(null)}
        />
      )}
      <ConfirmDialog
        open={Boolean(deleting) && canWrite}
        onOpenChange={(open) => {
          if (!open && !remove.isPending) setDeleting(null)
        }}
        title={t('Delete Provider')}
        desc={
          deleting?.channel_count
            ? t(
                'Reassign or delete this provider’s channels before deleting the provider.'
              )
            : t(
                'Delete provider "{{name}}"? Historical provider records will be retained.',
                { name: deleting?.name }
              )
        }
        confirmText={t('Delete')}
        destructive
        disabled={!canWrite || !deleting || deleting.channel_count > 0}
        isLoading={remove.isPending}
        handleConfirm={() => {
          if (deleting && canWrite) remove.mutate(deleting.id)
        }}
      >
        {remove.isError && (
          <Alert variant='destructive'>
            <AlertDescription>
              {t(getServerErrorMessage(remove.error))}
            </AlertDescription>
          </Alert>
        )}
      </ConfirmDialog>
    </>
  )
}
