export type SafeguardType = 'alarm' | 'interlock' | 'relief' | 'procedural' | 'containment' | 'detection'
export type SafeguardLifecycle = 'pending' | 'active' | 'expired' | 'invalid' | 'suspended'

export interface Safeguard {
  id: number
  name: string
  safeguard_type: SafeguardType
  target_scenario_id: number
  independence_key: string
  effectiveness: number
  test_interval_days: number
  last_verified_at: string | null
  lifecycle_state: SafeguardLifecycle
  evidence_note: string
  suspended_at?: string
  suspended_by?: number
  suspension_reason?: string
  alternative_measure?: string
  planned_restore_at?: string
  verification_expires_at?: string
  verification_expired?: boolean
  target_scenario?: { id: number; parameter: string; scenario_state: string }
}

export interface SafeguardInput {
  name: string
  safeguard_type: SafeguardType
  target_scenario_id: number
  independence_key: string
  effectiveness: number
  test_interval_days: number
  last_verified_at: string | null
  evidence_note: string
}

export interface SafeguardSuspendInput {
  reason: string
  alternative_measure: string
  planned_restore_at: string
}

export interface SafeguardResumeInput {
  verified_at: string
  evidence_note: string
}
