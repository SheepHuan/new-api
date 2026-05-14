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
import { useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import {
  getCoreRowModel,
  getSortedRowModel,
  type SortingState,
  useReactTable,
} from '@tanstack/react-table'
import { RefreshCw } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { cn } from '@/lib/utils'
import { Button } from '@/components/ui/button'
import { DataTablePage } from '@/components/data-table'
import { StatusBadge } from '@/components/status-badge'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table'
import { getQueueLogs } from '../api'
import type { QueueChannelLog, QueueLog } from '../types'
import { useQueueLogsColumns } from './queue-logs-columns'

export function QueueLogsTable() {
  const { t } = useTranslation()
  const columns = useQueueLogsColumns()
  const [sorting, setSorting] = useState<SortingState>([
    { id: 'enqueued_at', desc: false },
  ])

  const { data, isLoading, isFetching, refetch } = useQuery({
    queryKey: ['queue-logs'],
    queryFn: async () => {
      const result = await getQueueLogs()
      if (!result.success) {
        throw new Error(result.message || t('Failed to load queue logs'))
      }
      return result.data || { items: [], channels: [] }
    },
    refetchInterval: 2000,
    refetchIntervalInBackground: false,
  })

  const logs = data?.items || []
  const channels = data?.channels || []
  const table = useReactTable({
    data: logs,
    columns,
    state: { sorting },
    onSortingChange: setSorting,
    enableRowSelection: false,
    getCoreRowModel: getCoreRowModel(),
    getSortedRowModel: getSortedRowModel(),
  })

  return (
    <DataTablePage
      table={table}
      columns={columns}
      isLoading={isLoading}
      isFetching={isFetching}
      emptyTitle={t('No Queued Requests')}
      emptyDescription={t(
        'Requests waiting for scheduler dispatch will appear here.'
      )}
      skeletonKeyPrefix='queue-log-skeleton'
      tableClassName='max-h-[calc(100dvh-13rem)] overflow-auto sm:max-h-[calc(100dvh-14rem)]'
      tableHeaderClassName='bg-muted/30 sticky top-0 z-10'
      showPagination={false}
      toolbar={
        <div className='space-y-3 rounded-lg border bg-background p-3'>
          <div className='flex flex-col gap-2 sm:flex-row sm:items-center sm:justify-between'>
            <div className='text-sm font-medium'>
              {t('Queued requests: {{count}}', { count: logs.length })}
            </div>
            <Button
              type='button'
              variant='outline'
              size='sm'
              disabled={isFetching}
              onClick={() => void refetch()}
            >
              <RefreshCw
                className={cn('h-4 w-4', isFetching && 'animate-spin')}
              />
              {t('Refresh')}
            </Button>
          </div>
          <QueueChannelSummaryTable channels={channels} />
        </div>
      }
      getRowClassName={(row) => {
        const item = row.original as QueueLog
        return item.waiting_seconds >= 300
          ? 'bg-amber-50/30 dark:bg-amber-950/10'
          : undefined
      }}
    />
  )
}

function QueueChannelSummaryTable({
  channels,
}: {
  channels: QueueChannelLog[]
}) {
  const { t } = useTranslation()

  return (
    <div className='overflow-hidden rounded-md border'>
      <Table>
        <TableHeader className='bg-muted/30'>
          <TableRow>
            <TableHead className='h-8 text-xs'>{t('Channel')}</TableHead>
            <TableHead className='h-8 text-right text-xs'>
              {t('Queued Requests')}
            </TableHead>
            <TableHead className='h-8 text-right text-xs'>
              {t('Queued Users')}
            </TableHead>
            <TableHead className='h-8 text-right text-xs'>
              {t('Current RPM')}
            </TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {channels.length === 0 ? (
            <TableRow>
              <TableCell
                colSpan={4}
                className='text-muted-foreground h-10 text-center text-xs'
              >
                {t('No active queue channels')}
              </TableCell>
            </TableRow>
          ) : (
            channels.map((channel) => (
              <TableRow key={channel.channel_id}>
                <TableCell className='py-2'>
                  <div className='flex max-w-[220px] flex-col gap-0.5'>
                    <StatusBadge
                      label={`#${channel.channel_id}`}
                      autoColor={String(channel.channel_id)}
                      copyText={String(channel.channel_id)}
                      size='sm'
                      className='font-mono'
                    />
                    {channel.channel_name && (
                      <span className='text-muted-foreground/70 truncate text-[11px]'>
                        {channel.channel_name}
                      </span>
                    )}
                  </div>
                </TableCell>
                <TableCell className='py-2 text-right font-mono text-xs tabular-nums'>
                  {channel.queued_request_count.toLocaleString()}
                </TableCell>
                <TableCell className='py-2 text-right font-mono text-xs tabular-nums'>
                  {channel.queued_user_count.toLocaleString()}
                </TableCell>
                <TableCell className='py-2 text-right font-mono text-xs tabular-nums'>
                  {channel.current_rpm.toLocaleString()}
                </TableCell>
              </TableRow>
            ))
          )}
        </TableBody>
      </Table>
    </div>
  )
}
