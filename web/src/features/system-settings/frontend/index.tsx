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

import { SectionPageLayout } from '@/components/layout'
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from '@/components/ui/card'

export function FrontendSettings() {
  const { t } = useTranslation()
  return (
    <SectionPageLayout>
      <SectionPageLayout.Title>
        {t('Frontend Configuration')}
      </SectionPageLayout.Title>
      <SectionPageLayout.Content>
        <Card className='max-w-5xl'>
          <CardHeader>
            <CardTitle>{t('Automatic public platform data')}</CardTitle>
            <CardDescription>
              {t(
                'Public statistics are calculated from actual channels, evaluation samples and consumption logs.'
              )}
            </CardDescription>
          </CardHeader>
          <CardContent>
            <p className='text-muted-foreground text-sm'>
              {t(
                'Manual display values and homepage model overrides are not used. Existing manual records are retained but do not affect public statistics.'
              )}
            </p>
          </CardContent>
        </Card>
      </SectionPageLayout.Content>
    </SectionPageLayout>
  )
}
