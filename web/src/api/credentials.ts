import client from './client'

export type CredentialKind =
  | 'postgres_superuser'
  | 'postgres_replication'
  | 'netbox_db_user'
  | 'redis_password'
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
}

export const credentialLabels: Record<CredentialKind, string> = {
  postgres_superuser: 'Postgres Superuser',
  postgres_replication: 'Postgres Replication',
  netbox_db_user: 'NetBox DB User',
  redis_password: 'Redis Password',
  patroni_rest_password: 'Patroni REST API',
}
