/*
Copyright (C) 2023-2026 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as published by
the Free Software Foundation, either version 3 of the License, or
(at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program. If not, see <https://www.gnu.org/licenses/>.

For commercial licensing, please contact support@quantumnous.com
*/
import {
  createAuthClient,
  switchAccountAndNavigate,
} from '@knowyet/sdk/browser'

import { api } from '@/lib/api'

const accountSwitchAuth = createAuthClient({
  async json<T>(path: string, init?: RequestInit): Promise<T> {
    if (path !== '/auth/switch-account' || init?.method !== 'POST') {
      throw new Error('Unsupported platform authentication operation')
    }
    const response = await api.post(
      '/api/user/auth/switch-account',
      undefined,
      {
        skipAuthRefresh: true,
        skipErrorHandler: true,
      }
    )
    return response.data as T
  },
})

export async function switchAccount(): Promise<void> {
  await switchAccountAndNavigate(accountSwitchAuth)
}
