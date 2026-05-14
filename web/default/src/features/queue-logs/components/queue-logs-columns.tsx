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
import { useMemo } from 'react'
import type { ColumnDef } from '@tanstack/react-table'
import { KeyRound } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { getUserAvatarFallback, getUserAvatarStyle } from '@/lib/avatar'
import { formatTimestampToDate } from '@/lib/format'
import { cn } from '@/lib/utils'
import { Avatar, AvatarFallback } from '@/components/ui/avatar'
import { Badge } from '@/components/ui/badge'
import {
  Tooltip,
  TooltipContent,
  TooltipProvider,
  TooltipTrigger,
} from '@/components/ui/tooltip'
import { DataTableColumnHeader } from '@/components/data-table'
import { StatusBadge } from '@/components/status-badge'
import type { QueueLog } from '../types'

function formatWaitingSeconds(seconds: number): string {
  const normalized = Math.max(0, Math.floor(seconds || 0))
  if (normalized < 60) return `${normalized}s`
  const minutes = Math.floor(normalized / 60)
  const remainingSeconds = normalized % 60
  if (minutes < 60) return `${minutes}m ${remainingSeconds}s`
  const hours = Math.floor(minutes / 60)
  const remainingMinutes = minutes % 60
  return `${hours}h ${remainingMinutes}m`
}

function waitingPillClass(seconds: number) {
  if (seconds >= 300) {
    return 'border-amber-200/70 bg-amber-50/70 text-amber-700 dark:border-amber-800/50 dark:bg-amber-950/25 dark:text-amber-400'
  }
  if (seconds >= 60) {
    return 'border-sky-200/70 bg-sky-50/70 text-sky-700 dark:border-sky-800/50 dark:bg-sky-950/25 dark:text-sky-400'
  }
  return 'border-emerald-200/70 bg-emerald-50/70 text-emerald-700 dark:border-emerald-800/50 dark:bg-emerald-950/25 dark:text-emerald-400'
}

export function useQueueLogsColumns(): ColumnDef<QueueLog>[] {
  const { t } = useTranslation()

  return useMemo<ColumnDef<QueueLog>[]>(
    () => [
      {
        accessorKey: 'enqueued_at',
        header: ({ column }) => (
          <DataTableColumnHeader column={column} title={t('Queued At')} />
        ),
        cell: ({ row }) => (
          <span className='font-mono text-xs tabular-nums'>
            {formatTimestampToDate(row.original.enqueued_at)}
          </span>
        ),
        meta: { label: t('Queued At') },
      },
      {
        id: 'user',
        accessorFn: (row) => row.username || String(row.user_id || ''),
        header: ({ column }) => (
          <DataTableColumnHeader column={column} title={t('User')} />
        ),
        cell: ({ row }) => {
          const item = row.original
          const username = item.username || `#${item.user_id}`
          return (
            <div className='flex min-w-0 items-center gap-1.5'>
              <Avatar className='ring-border/60 size-6 shrink-0 ring-1'>
                <AvatarFallback
                  className='text-[11px] font-semibold'
                  style={
                    item.username ? getUserAvatarStyle(username) : undefined
                  }
                >
                  {item.username ? getUserAvatarFallback(username) : '#'}
                </AvatarFallback>
              </Avatar>
              <span className='text-muted-foreground truncate text-sm'>
                {username}
              </span>
            </div>
          )
        },
        meta: { label: t('User') },
      },
      {
        accessorKey: 'token_name',
        header: ({ column }) => (
          <DataTableColumnHeader column={column} title={t('Token')} />
        ),
        cell: ({ row }) => {
          const item = row.original
          if (!item.token_name) {
            return <span className='text-muted-foreground/60 text-xs'>-</span>
          }

          return (
            <div className='flex max-w-[150px] flex-col gap-0.5'>
              <StatusBadge
                label={item.token_name}
                icon={KeyRound}
                copyText={item.token_name}
                size='sm'
                showDot={false}
                className='border-border/60 bg-muted/30 text-foreground max-w-full overflow-hidden rounded-md border px-1.5 py-0.5 font-mono'
              />
              {item.token_group && (
                <span className='text-muted-foreground/60 truncate text-[11px]'>
                  {item.token_group}
                </span>
              )}
            </div>
          )
        },
        meta: { label: t('Token') },
      },
      {
        id: 'waiting_seconds',
        accessorKey: 'waiting_seconds',
        header: ({ column }) => (
          <DataTableColumnHeader column={column} title={t('Waiting')} />
        ),
        cell: ({ row }) => {
          const seconds = row.original.waiting_seconds
          return (
            <span
              className={cn(
                'inline-flex h-6 items-center rounded-full border px-2 font-mono text-xs font-medium tabular-nums',
                waitingPillClass(seconds)
              )}
            >
              {formatWaitingSeconds(seconds)}
            </span>
          )
        },
        meta: { label: t('Waiting') },
      },
      {
        id: 'channel',
        accessorFn: (row) =>
          `${row.channel_name || ''} ${row.channel_id || ''}`.trim(),
        header: ({ column }) => (
          <DataTableColumnHeader column={column} title={t('Channel')} />
        ),
        cell: ({ row }) => {
          const item = row.original
          const channelIdDisplay = `#${item.channel_id}`
          return (
            <TooltipProvider>
              <Tooltip>
                <TooltipTrigger
                  render={
                    <div className='flex max-w-[160px] flex-col gap-0.5' />
                  }
                >
                  <StatusBadge
                    label={channelIdDisplay}
                    autoColor={String(item.channel_id)}
                    copyText={String(item.channel_id)}
                    size='sm'
                    className='font-mono'
                  />
                  {item.channel_name && (
                    <span className='text-muted-foreground/70 truncate text-[11px]'>
                      {item.channel_name}
                    </span>
                  )}
                </TooltipTrigger>
                <TooltipContent>
                  <p>
                    {item.channel_name
                      ? `${item.channel_name} ${channelIdDisplay}`
                      : channelIdDisplay}
                  </p>
                </TooltipContent>
              </Tooltip>
            </TooltipProvider>
          )
        },
        meta: { label: t('Channel') },
      },
      {
        accessorKey: 'queue_position',
        header: ({ column }) => (
          <DataTableColumnHeader column={column} title={t('Queue Position')} />
        ),
        cell: ({ row }) => (
          <Badge variant='secondary' className='font-mono'>
            {row.original.queue_position}
          </Badge>
        ),
        meta: { label: t('Queue Position') },
      },
    ],
    [t]
  )
}
