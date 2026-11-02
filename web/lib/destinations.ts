// A destination is edited through the same form that adds one, and the api sends a test
// message whenever a webhook or a recipient list changes. So an edit sends only the
// fields that actually changed: saving an untouched form would mail everyone on the list
// to prove nothing, and a stray comma is not a new recipient. The cost of getting that
// wrong lands in somebody's inbox, so it lives here where it can be tested.

export type EditedDestination = { kind: string; name: string; config: { to?: string[] } }
export type DestinationForm = { name: string; webhook: string; to: string }

export function recipients(s: string): string[] {
  return s
    .split(',')
    .map((t) => t.trim())
    .filter(Boolean)
}

export function destinationPatch(d: EditedDestination, form: DestinationForm): Record<string, unknown> {
  const body: Record<string, unknown> = {}
  if (form.name.trim() !== d.name) body.name = form.name.trim()
  // An empty webhook field means "keep the one it has": the api never sends the stored
  // webhook back, so the form cannot show it and silence has to mean unchanged.
  if (d.kind === 'slack' && form.webhook.trim()) body.webhook_url = form.webhook.trim()
  const to = recipients(form.to)
  if (d.kind === 'email' && to.join(',') !== (d.config?.to ?? []).join(',')) body.to = to
  return body
}
