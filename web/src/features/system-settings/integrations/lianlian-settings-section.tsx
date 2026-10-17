import { useFormContext } from 'react-hook-form'
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
import { useTranslation } from 'react-i18next'

import {
  FormControl,
  FormDescription,
  FormField,
  FormItem,
  FormLabel,
  FormMessage,
} from '@/components/ui/form'
import { Input } from '@/components/ui/input'
import {
  Select,
  SelectContent,
  SelectGroup,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { Switch } from '@/components/ui/switch'
import { Textarea } from '@/components/ui/textarea'

import {
  SettingsSwitchContent,
  SettingsSwitchItem,
} from '../components/settings-form-layout'
import {
  DEFAULT_LIANLIAN_BUSI_TYPE,
  type LianlianSettingsValues,
} from './lianlian-settings'

interface LianlianSettingsSectionProps {
  values: LianlianSettingsValues
  onValueChange: <K extends keyof LianlianSettingsValues>(
    key: K,
    value: LianlianSettingsValues[K]
  ) => void
}

export function LianlianSettingsSection(props: LianlianSettingsSectionProps) {
  const { t } = useTranslation()
  const { control } = useFormContext<LianlianSettingsValues>()

  return (
    <div className='space-y-4'>
      <div>
        <h3 className='text-lg font-medium'>{t('LianLian Gateway')}</h3>
        <p className='text-muted-foreground text-sm'>
          {t(
            'Native Alipay and WeChat scan-code payments through LianLian acquiring.'
          )}
        </p>
      </div>

      <div className='rounded-md bg-blue-50 p-4 text-sm text-blue-900 dark:bg-blue-950 dark:text-blue-100'>
        <p className='mb-2 font-medium'>{t('Webhook Configuration:')}</p>
        <ul className='list-inside list-disc space-y-1'>
          <li>
            {t('Webhook URL:')}{' '}
            <code className='rounded bg-blue-100 px-1 py-0.5 text-xs dark:bg-blue-900'>
              {'<ServerAddress>/api/user/lianlian/notify'}
            </code>
          </li>
          <li>
            {t(
              'Register this notify URL in the LianLian merchant portal. The platform public key is bundled, so only replace it when LianLian rotates the key.'
            )}
          </li>
        </ul>
      </div>

      <FormField
        control={control}
        name='LianlianEnabled'
        render={() => (
          <FormItem>
            <SettingsSwitchItem>
              <SettingsSwitchContent>
                <FormLabel>{t('Enable LianLian')}</FormLabel>
                <FormDescription>
                  {t(
                    'Native Alipay and WeChat scan-code payments through LianLian acquiring.'
                  )}
                </FormDescription>
              </SettingsSwitchContent>
              <FormControl>
                <Switch
                  checked={props.values.LianlianEnabled}
                  onCheckedChange={(checked) =>
                    props.onValueChange('LianlianEnabled', checked)
                  }
                />
              </FormControl>
            </SettingsSwitchItem>
          </FormItem>
        )}
      />

      <div className='grid gap-6 md:grid-cols-2'>
        <FormField
          control={control}
          name='LianlianEnv'
          render={() => (
            <FormItem>
              <FormLabel>{t('Environment')}</FormLabel>
              <Select
                items={[
                  { value: 'test', label: t('Sandbox') },
                  { value: 'prod', label: t('Production') },
                ]}
                value={props.values.LianlianEnv === 'prod' ? 'prod' : 'test'}
                onValueChange={(value) => {
                  if (value !== null) {
                    props.onValueChange('LianlianEnv', value)
                  }
                }}
              >
                <FormControl>
                  <SelectTrigger>
                    <SelectValue placeholder={t('Sandbox')} />
                  </SelectTrigger>
                </FormControl>
                <SelectContent alignItemWithTrigger={false}>
                  <SelectGroup>
                    <SelectItem value='test'>{t('Sandbox')}</SelectItem>
                    <SelectItem value='prod'>{t('Production')}</SelectItem>
                  </SelectGroup>
                </SelectContent>
              </Select>
              <FormDescription>
                {t('Selects the default LianLian gateway')}
              </FormDescription>
              <FormMessage />
            </FormItem>
          )}
        />

        <FormField
          control={control}
          name='LianlianBusiType'
          render={() => (
            <FormItem>
              <FormLabel>{t('Business type')}</FormLabel>
              <FormControl>
                <Input
                  placeholder={DEFAULT_LIANLIAN_BUSI_TYPE}
                  value={props.values.LianlianBusiType}
                  onChange={(event) =>
                    props.onValueChange('LianlianBusiType', event.target.value)
                  }
                />
              </FormControl>
              <FormDescription>
                {t(
                  'LianLian busi_type, leave blank for the default ordinary acquiring (100001)'
                )}
              </FormDescription>
              <FormMessage />
            </FormItem>
          )}
        />
      </div>

      <FormField
        control={control}
        name='LianlianMchId'
        render={() => (
          <FormItem>
            <FormLabel>{t('Merchant ID')}</FormLabel>
            <FormControl>
              <Input
                placeholder='2020042200284052'
                autoComplete='off'
                value={props.values.LianlianMchId}
                onChange={(event) =>
                  props.onValueChange('LianlianMchId', event.target.value)
                }
              />
            </FormControl>
            <FormMessage />
          </FormItem>
        )}
      />

      <FormField
        control={control}
        name='LianlianGateway'
        render={() => (
          <FormItem>
            <FormLabel>{t('Custom gateway')}</FormLabel>
            <FormControl>
              <Input
                placeholder={t(
                  'Leave blank to use the official LianLian gateway'
                )}
                value={props.values.LianlianGateway}
                onChange={(event) =>
                  props.onValueChange('LianlianGateway', event.target.value)
                }
              />
            </FormControl>
            <FormDescription>
              {t(
                'Optional LianLian gateway base URL. It must end with /mch, e.g. https://openapi-test2.lianlianpay-inc.com/mch (test) or https://openapi.lianlianpay.com/mch (production); the create-order path is appended automatically.'
              )}
            </FormDescription>
            <FormMessage />
          </FormItem>
        )}
      />

      <div className='grid gap-6 md:grid-cols-2'>
        <FormField
          control={control}
          name='LianlianPrivateKey'
          render={() => (
            <FormItem>
              <FormLabel>{t('Merchant RSA private key')}</FormLabel>
              <FormControl>
                <Textarea
                  rows={4}
                  placeholder='-----BEGIN RSA PRIVATE KEY-----'
                  autoComplete='new-password'
                  className='font-mono text-xs'
                  value={props.values.LianlianPrivateKey}
                  onChange={(event) =>
                    props.onValueChange(
                      'LianlianPrivateKey',
                      event.target.value
                    )
                  }
                />
              </FormControl>
              <FormDescription>
                {t(
                  'PEM encoded private key used to sign create-order requests. Leave blank unless rotating the key.'
                )}
              </FormDescription>
              <FormMessage />
            </FormItem>
          )}
        />

        <FormField
          control={control}
          name='LianlianPlatformPublicCert'
          render={() => (
            <FormItem>
              <FormLabel>{t('LianLian platform public key')}</FormLabel>
              <FormControl>
                <Textarea
                  rows={4}
                  placeholder={t('Leave blank to use the bundled key')}
                  className='font-mono text-xs'
                  value={props.values.LianlianPlatformPublicCert}
                  onChange={(event) =>
                    props.onValueChange(
                      'LianlianPlatformPublicCert',
                      event.target.value
                    )
                  }
                />
              </FormControl>
              <FormDescription>
                {t(
                  'PEM public key used to verify payment notifications. The official key is bundled.'
                )}
              </FormDescription>
              <FormMessage />
            </FormItem>
          )}
        />
      </div>
    </div>
  )
}
