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
export type QueueLog = {
  channel_id: number
  channel_name: string
  user_id: number
  username: string
  token_name: string
  token_group: string
  enqueued_at: number
  waiting_seconds: number
  queue_position: number
}

export type QueueChannelLog = {
  channel_id: number
  channel_name: string
  queued_request_count: number
  queued_user_count: number
  current_rpm: number
}

export type QueueLogsData = {
  items: QueueLog[]
  channels: QueueChannelLog[]
}

export type QueueLogsResponse = {
  success: boolean
  message?: string
  data?: QueueLogsData
}
