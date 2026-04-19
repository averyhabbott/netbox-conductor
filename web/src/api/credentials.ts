import client from './client'

export type CredentialKind =
  | 'postgres_superuser'
  | 'postgres_replication'
  | 'netbox_db_user'
  | 'redis_tasks_password'
  | 'redis_caching_password'
  | 'netbox_secret_key'
  | 'netbox_api_token_pepper'
  | 'patroni_rest_password'

export interface Credential {
  id: string
  cluster_id: string
  kind: CredentialKind
  username: string
  db_name?: string
  created_at: string
  rotated_at?: string
}

export interface GeneratedCredential {
  kind: CredentialKind
  username: string
  password: string
  db_name?: string
}

export interface GenerateCredentialsResponse {
  generated: GeneratedCredential[]
  warning: string
}

export const credentialsApi = {
  list: (clusterId: string) =>
    client.get<Credential[]>(`/clusters/${clusterId}/credentials`).then((r) => r.data),

  upsert: (
    clusterId: string,
    kind: CredentialKind,
    body: { username: string; password: string; db_name?: string }
  ) =>
    client
      .put<Credential>(`/clusters/${clusterId}/credentials/${kind}`, body)
      .then((r) => r.data),

  generateCredentials: (clusterId: string, missingOnly = false) =>
    client
      .post<GenerateCredentialsResponse>(
        `/clusters/${clusterId}/credentials/generate${missingOnly ? '?missing_only=true' : ''}`
      )
      .then((r) => r.data),
}

export const credentialLabels: Record<CredentialKind, string> = {
  postgres_superuser: 'Postgres Superuser',
  postgres_replication: 'Postgres Replication',
  netbox_db_user: 'NetBox DB User',
  redis_tasks_password: 'Redis Password (Tasks)',
  redis_caching_password: 'Redis Password (Caching)',
  netbox_secret_key: 'NetBox Secret Key',
  netbox_api_token_pepper: 'API Token Pepper',
  patroni_rest_password: 'Patroni REST API',
}

// Credential kinds that are single-value secrets (no username field).
export const secretOnlyKinds: ReadonlySet<CredentialKind> = new Set([
  'netbox_secret_key',
  'netbox_api_token_pepper',
  'redis_tasks_password',
  'redis_caching_password',
])
