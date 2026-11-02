import { describe, expect, it } from 'vitest'

import { destinationPatch, recipients } from './destinations'

const slack = { kind: 'slack', name: '#marketing', config: {} }
const email = { kind: 'email', name: 'Marketing team', config: { to: ['cmo@acme.com', 'team@acme.com'] } }

// The form opens prefilled with what the destination already is, so pressing Save
// without touching anything is the common case, and it must cost nothing.
describe('an untouched form', () => {
  it('sends nothing at all', () => {
    expect(destinationPatch(slack, { name: '#marketing', webhook: '', to: '' })).toEqual({})
    expect(destinationPatch(email, { name: 'Marketing team', webhook: '', to: 'cmo@acme.com, team@acme.com' })).toEqual({})
  })

  it('still sends nothing when the recipients are respaced or carry a stray comma', () => {
    expect(destinationPatch(email, { name: 'Marketing team', webhook: '', to: 'cmo@acme.com,team@acme.com,' })).toEqual({})
    expect(destinationPatch(email, { name: 'Marketing team', webhook: '', to: '  cmo@acme.com ,  team@acme.com  ' })).toEqual({})
  })

  it('sends nothing for a webhook field holding only spaces', () => {
    expect(destinationPatch(slack, { name: '#marketing', webhook: '   ', to: '' })).toEqual({})
  })
})

describe('a changed form', () => {
  it('sends the new name on its own', () => {
    expect(destinationPatch(slack, { name: ' #growth ', webhook: '', to: '' })).toEqual({ name: '#growth' })
  })

  it('sends a replacement webhook, trimmed', () => {
    expect(destinationPatch(slack, { name: '#marketing', webhook: ' https://hooks.slack.com/services/new ', to: '' })).toEqual({
      webhook_url: 'https://hooks.slack.com/services/new',
    })
  })

  it('sends the whole recipient list when one changes', () => {
    expect(destinationPatch(email, { name: 'Marketing team', webhook: '', to: 'cmo@acme.com, board@acme.com' })).toEqual({
      to: ['cmo@acme.com', 'board@acme.com'],
    })
  })

  it('sends an empty list when the last recipient is cleared, so the api refuses rather than the form guessing', () => {
    expect(destinationPatch(email, { name: 'Marketing team', webhook: '', to: '' })).toEqual({ to: [] })
  })
})

// A field the other kind does not have is not a change: a kind cannot be switched once
// the destination exists, and the api rejects the wrong field for the kind.
describe('fields belonging to the other kind', () => {
  it('are ignored', () => {
    expect(destinationPatch(slack, { name: '#marketing', webhook: '', to: 'someone@acme.com' })).toEqual({})
    expect(destinationPatch(email, { name: 'Marketing team', webhook: 'https://hooks.slack.com/services/x', to: 'cmo@acme.com, team@acme.com' })).toEqual({})
  })
})

describe('recipients', () => {
  it('drops blanks instead of sending an empty address', () => {
    expect(recipients('a@acme.com, , b@acme.com')).toEqual(['a@acme.com', 'b@acme.com'])
    expect(recipients('')).toEqual([])
    expect(recipients(' , ')).toEqual([])
  })
})
