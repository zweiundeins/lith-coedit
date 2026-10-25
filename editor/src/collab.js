// The browser side of the commit protocol, after
// @stepwisehq/prosemirror-collab-commit 1.0.5 (MIT, (c) 2023 Greg Zapp; it
// builds on prosemirror-collab, MIT, (c) Marijn Haverbeke and others).
//
// Differences from collab-commit:
// - Each unconfirmed step carries a sequence number, so a commit knows which
//   steps it holds even after rebases dropped some of them.
// - The commit in flight is kept by the editor, not mutated into the state.
// - Our own commit comes back with the steps as the writer applied them. When
//   they equal our rebased steps (the normal case) they only confirm; when they
//   differ, receive() undoes our steps, applies the writer's and rebases the
//   rest on top, so the confirmed document always equals the writer's.
// - A refused commit's steps are taken out the same way, with no steps over.
import { Plugin, PluginKey } from 'prosemirror-state'
import { Step } from 'prosemirror-transform'

export const collabKey = new PluginKey('collab')
// Set on every transaction this module makes: no limit check, no history.
export const REMOTE = 'sb-remote'

export class Rebaseable {
	constructor(step, inverted, origin, seq) {
		this.step = step
		this.inverted = inverted
		this.origin = origin
		this.seq = seq
	}
}

export class CollabState {
	// version: the stream version of the newest commit applied.
	// unconfirmed: local steps the writer has not confirmed, oldest first.
	// seq: the sequence number of the newest local step.
	constructor(version, unconfirmed, seq) {
		this.version = version
		this.unconfirmed = unconfirmed
		this.seq = seq
	}
}

export function collab({ version = 0 } = {}) {
	return new Plugin({
		key: collabKey,
		state: {
			init: () => new CollabState(version, [], 0),
			apply(tr, st) {
				const next = tr.getMeta(collabKey)
				if (next) return next
				if (!tr.docChanged) return st
				let seq = st.seq
				const add = tr.steps.map((s, i) => new Rebaseable(s, s.invert(tr.docs[i]), tr, ++seq))
				return new CollabState(st.version, st.unconfirmed.concat(add), seq)
			},
		},
		// prosemirror-history keeps one item per step, so it can rebase them.
		historyPreserveItems: true,
	})
}

export const collabState = (state) => collabKey.getState(state)
export const getVersion = (state) => collabKey.getState(state).version

// A new commit of at most max unconfirmed steps, or null.
export function makeCommit(state, max = 20) {
	const st = collabKey.getState(state)
	if (!st.unconfirmed.length) return null
	const take = st.unconfirmed.slice(0, max)
	return { base: st.version, ref: randomRef(), steps: take.map((u) => u.step.toJSON()), last: take[take.length - 1].seq }
}

// The unconfirmed steps a commit still holds (rebases may have dropped some).
export const stepsOf = (state, last) => collabKey.getState(state).unconfirmed.filter((u) => u.seq <= last)

// confirms reports whether applied (the writer's steps of our commit) equal
// what we hold of it, step by step, after a round through prosemirror's JSON.
export function confirms(state, last, applied) {
	const mine = stepsOf(state, last)
	if (mine.length !== applied.length) return false
	for (let i = 0; i < mine.length; i++) if (JSON.stringify(mine[i].step.toJSON()) !== JSON.stringify(applied[i].toJSON())) return false
	return true
}

// confirmTransaction moves to the next version and drops our commit's steps,
// which the document already shows.
export function confirmTransaction(state, last) {
	const st = collabKey.getState(state)
	const next = new CollabState(st.version + 1, st.unconfirmed.filter((u) => u.seq > last), st.seq)
	return state.tr.setMeta(collabKey, next).setMeta('addToHistory', false).setMeta(REMOTE, true)
}

// receiveTransaction applies steps (a commit as the writer applied it) under
// our unconfirmed ones: it undoes them, applies steps, and maps the ones with
// a sequence number above drop back on top. version is the new version (null
// keeps it). It returns null when steps do not apply: the documents differ.
export function receiveTransaction(state, steps, { drop = 0, version = null } = {}) {
	const st = collabKey.getState(state)
	const tr = state.tr
	const n = st.unconfirmed.length
	for (let i = n - 1; i >= 0; i--) if (tr.maybeStep(st.unconfirmed[i].inverted).failed) return null
	for (const s of steps) if (tr.maybeStep(s).failed) return null
	const kept = []
	for (let i = 0, mapFrom = n; i < n; i++) {
		const u = st.unconfirmed[i]
		const map = tr.mapping.slice(mapFrom)
		mapFrom--
		if (u.seq <= drop) continue
		const mapped = u.step.map(map)
		if (mapped && !tr.maybeStep(mapped).failed) {
			tr.mapping.setMirror(mapFrom, tr.steps.length - 1)
			kept.push(new Rebaseable(mapped, mapped.invert(tr.docs[tr.docs.length - 1]), u.origin, u.seq))
		}
	}
	const next = new CollabState(version ?? st.version, kept, st.seq)
	return tr.setMeta('rebased', n).setMeta('addToHistory', false).setMeta(collabKey, next).setMeta(REMOTE, true)
}

// batchTransaction turns one commit of a frame ({v, ref, steps}, the next
// version) into a transaction. inflight is our commit in flight, or null.
// It returns {tr, own, reconciled, steps}; tr is null when the commit does
// not apply (the documents differ) and it throws on steps it cannot parse.
export function batchTransaction(state, schema, b, inflight) {
	const steps = b.steps.map((j) => Step.fromJSON(schema, j))
	const version = getVersion(state) + 1
	if (inflight && b.ref === inflight.ref) {
		if (confirms(state, inflight.last, steps)) return { tr: confirmTransaction(state, inflight.last), own: true, reconciled: false, steps }
		return { tr: receiveTransaction(state, steps, { drop: inflight.last, version }), own: true, reconciled: true, steps }
	}
	return { tr: receiveTransaction(state, steps, { version }), own: false, reconciled: false, steps }
}

export function randomRef() {
	const b = new Uint32Array(2)
	globalThis.crypto.getRandomValues(b)
	return b[0].toString(36) + b[1].toString(36)
}
