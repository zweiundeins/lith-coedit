// The commit protocol of one editor (README, "The protocol"): it reads the
// host's attributes that the server's frames morph, applies the commits
// they carry, sends one commit at a time and the presence, and stops when
// the document was reset, removed or folded away. The app's editor owns
// the ProseMirror view (with collab, caretPlugin and commentPlugin among
// its plugins), the toolbar and the notices; the session tells it what
// happened through emit.
//
// emit(type, detail), types:
//   commit    {epoch, base, ref, steps, tab?}  post it; call posted() when it settled
//   presence  {epoch, ack, v, anchor, head, seq, tab?}
//   ack       {epoch, ack}                      a frame's commits were applied
//   refused   {ref, message, dropped}           a refusal arrived
//   stopped   {state, unsaved}                  reset, gone, gap, readonly or diverged
//   resumed   {}                                readonly ended
//   throttled {}                                the server answered 429
import { Mapping } from 'prosemirror-transform'
import { collabState, getVersion, makeCommit, batchTransaction, receiveTransaction, REMOTE } from './collab.js'
import { Carets } from './carets.js'
import { drawComments, commentRange } from './comments.js'

const parseJSON = (s, fallback) => {
	if (typeof s !== 'string' || !s) return fallback
	try {
		return JSON.parse(s)
	} catch {
		return fallback
	}
}

export class Session {
	// view: the EditorView; schema: its schema; read(): the host's
	// attributes {epoch, client, tab, state, readonly, batches, carets,
	// comments, refusal}; emit(type, detail). carets and comments are the
	// options of Carets and drawComments (class names).
	constructor({ view, schema, read, emit, carets = {}, comments = {}, maxSteps = 20, presenceMs = 100, confirmWait = 3000, backoff = [1000, 2000, 4000, 8000, 15000] }) {
		this.view = view
		this.schema = schema
		this.read = read
		this.emit = emit
		this.commentOpts = comments
		this.maxSteps = maxSteps
		this.presenceMs = presenceMs
		this.confirmWait = confirmWait
		this.backoff = backoff
		this.p = read()
		this.epoch = this.p.epoch
		this.inflight = null // {detail, last, tries, timer}
		this.stopped = null
		this.seq = 0
		this.raw = { batches: null, carets: null, refusal: null, comments: null }
		this.handledRefusals = new Set()
		this.stats = { resyncs: 0, resends: 0, commits: 0, received: 0, refused: 0 }
		this.presenceAt = 0
		this.presenceTimer = 0
		this.carets = new Carets(view, carets)
	}

	editable() {
		return !this.stopped && !this.p.readonly && this.p.state === 'ok'
	}

	// unsaved counts the steps the server has not confirmed.
	unsaved() {
		return collabState(this.view.state).unconfirmed.length
	}

	// transaction is called by the app's dispatchTransaction after the
	// view took the new state.
	transaction(tr) {
		if (this.destroyed) return
		if (tr.docChanged && !tr.getMeta(REMOTE)) this.send()
		if (tr.docChanged || tr.selectionSet) this.presence()
		if (tr.selectionSet && !tr.getMeta(REMOTE)) this.carets.tags()
	}

	// sync reads the host's attributes and applies what changed. The page's
	// frames morph them; a burst of attribute writes arrives as one call.
	// It returns false when the editor is stopped and the frame was left
	// alone.
	sync() {
		if (this.destroyed) return false
		const p = (this.p = this.read())
		if (this.stopped && this.stopped !== 'readonly') return false
		if (p.epoch && this.epoch && p.epoch !== this.epoch) return this.stop('reset'), false
		if (['reset', 'gone', 'gap'].includes(p.state)) return this.stop(p.state), false
		if (p.state === 'readonly' && !this.stopped) this.stop('readonly')
		else if (p.state === 'ok' && this.stopped === 'readonly') this.resume()

		let applied = false
		if (p.batches !== this.raw.batches) {
			const batches = parseJSON(p.batches, [])
			const done = this.receive(Array.isArray(batches) ? batches : [])
			if (done === false) return false
			applied = done > 0
			// A frame that left a gap is read again with the next one.
			if (!this.gapPending) this.raw.batches = p.batches
		}
		if (p.refusal !== this.raw.refusal) {
			this.raw.refusal = p.refusal
			if (p.refusal) this.refused(p.refusal)
		}
		if (applied || p.carets !== this.raw.carets) {
			this.raw.carets = p.carets
			this.carets.set(parseJSON(p.carets, []), p.client)
		}
		if (p.comments !== this.raw.comments || (applied && this.commentsPending)) {
			this.raw.comments = p.comments
			this.commentsPending = !drawComments(this.view, this.carets.maps, parseJSON(p.comments, null), this.commentOpts)
		}
		this.view.setProps({}) // editable may have changed
		if (applied) {
			this.emit('ack', { epoch: this.epoch, ack: getVersion(this.view.state) })
			this.presence()
		}
		this.send()
		return true
	}

	// receive applies the frame's commits in version order and returns how
	// many it applied, or false when the editor had to stop.
	receive(batches) {
		let n = 0
		this.gapPending = false
		const sorted = batches.filter((b) => b && Number.isInteger(b.v) && Array.isArray(b.steps)).sort((a, b) => a.v - b.v)
		for (const b of sorted) {
			const v = getVersion(this.view.state)
			if (b.v <= v) continue
			if (b.v !== v + 1) {
				this.gapPending = true // wait for the frame that brings v + 1
				break
			}
			let r
			try {
				r = batchTransaction(this.view.state, this.schema, b, this.inflight ? { ref: this.inflight.detail.ref, last: this.inflight.last } : null)
			} catch (e) {
				console.error('coedit: a commit from the server does not parse', b, e)
				this.stop('diverged')
				return false
			}
			if (!r.tr) {
				console.error('coedit: a commit from the server does not apply', b)
				this.stop('diverged')
				return false
			}
			if (r.own) {
				clearTimeout(this.inflight.timer)
				this.inflight = null
				if (r.reconciled) {
					this.stats.resyncs++
					console.warn('coedit: the server applied commit %s differently; the editor took the server\'s steps', b.ref)
				}
			} else this.stats.received++
			this.carets.confirmed(b.v, r.steps)
			this.view.dispatch(r.tr)
			n++
		}
		return n
	}

	// send emits our next commit unless one is in flight (one at a time, at
	// most maxSteps steps).
	send() {
		if (this.inflight || this.stopped || this.p.state !== 'ok' || this.p.readonly) return
		const c = makeCommit(this.view.state, this.maxSteps)
		if (!c) return
		const detail = { epoch: this.epoch, base: c.base, ref: c.ref, steps: c.steps }
		if (this.p.tab) detail.tab = this.p.tab
		this.inflight = { detail, last: c.last, tries: 0, timer: 0 }
		this.stats.commits++
		this.emit('commit', detail)
		// If nobody calls posted() (no page wiring), the commit still goes out again.
		this.inflight.timer = setTimeout(() => this.resend(), this.confirmWait * 2)
	}

	resend() {
		const f = this.inflight
		if (!f || this.stopped || this.p.state !== 'ok') return
		f.tries++
		this.stats.resends++
		this.emit('commit', f.detail)
		clearTimeout(f.timer)
		f.timer = setTimeout(() => this.resend(), this.confirmWait * 2)
	}

	// posted is called by the page when its post of commit ref settled: ok
	// when the server answered (Datastar's @post resolves for any status),
	// false on a network error. status, when the page has it, refines that.
	posted(ref, ok, status) {
		const f = this.inflight
		if (!f || f.detail.ref !== ref) return
		clearTimeout(f.timer)
		if (status === 422 || status === 409) {
			// The frame says why (refusal, or state); wait for it.
			f.timer = setTimeout(() => this.resend(), this.confirmWait * 3)
			return
		}
		if (!ok || status === 429 || (status >= 500 && status < 600)) {
			if (status === 429) this.emit('throttled', {})
			const wait = this.backoff[Math.min(f.tries, this.backoff.length - 1)]
			f.timer = setTimeout(() => this.resend(), wait)
			return
		}
		f.timer = setTimeout(() => this.resend(), this.confirmWait)
	}

	// refused handles the refusal attribute: {"ref", "message"} takes that
	// commit's steps out (the writer will never apply them); plain text only
	// tells.
	refused(raw) {
		const r = parseJSON(raw, null) ?? { message: String(raw) }
		const key = r.ref ?? raw
		if (this.handledRefusals.has(key)) return
		this.handledRefusals.add(key)
		const f = this.inflight
		let dropped = 0
		if (r.ref && f && f.detail.ref === r.ref) {
			const before = this.unsaved()
			const tr = receiveTransaction(this.view.state, [], { drop: f.last })
			clearTimeout(f.timer)
			this.inflight = null
			if (tr) {
				this.view.dispatch(tr)
				dropped = before - this.unsaved()
			}
			this.stats.refused++
		}
		this.emit('refused', { ref: r.ref ?? '', message: String(r.message || ''), dropped })
		this.send()
	}

	stop(state) {
		if (this.stopped === state) return
		this.stopped = state
		if (this.inflight) clearTimeout(this.inflight.timer)
		if (state !== 'readonly') {
			this.inflight = null
			clearTimeout(this.presenceTimer)
			this.presenceTimer = 0
		}
		this.view.setProps({})
		this.emit('stopped', { state, unsaved: this.unsaved() })
	}

	resume() {
		this.stopped = null
		this.emit('resumed', {})
		this.view.setProps({})
		this.send()
	}

	// presence sends our selection at the confirmed version, at most every
	// presenceMs.
	presence() {
		if (this.presenceTimer || (this.stopped && this.stopped !== 'readonly')) return
		const wait = Math.max(0, this.presenceMs - (Date.now() - this.presenceAt))
		this.presenceTimer = setTimeout(() => {
			this.presenceTimer = 0
			if (this.destroyed) return
			this.presenceAt = Date.now()
			const state = this.view.state
			const { version, unconfirmed } = collabState(state)
			const back = new Mapping(unconfirmed.map((u) => u.step.getMap())).invert()
			const { anchor, head } = state.selection
			const detail = { epoch: this.epoch, ack: version, v: version, anchor: back.map(anchor), head: back.map(head), seq: ++this.seq }
			if (this.p.tab) detail.tab = this.p.tab
			this.emit('presence', detail)
		}, wait)
	}

	// commentRange is the selection as an inline comment's range: at the
	// confirmed version, with the epoch; null without selected text.
	commentRange() {
		const r = commentRange(this.view.state)
		return r && { ...r, epoch: this.epoch }
	}

	destroy() {
		this.destroyed = true
		if (this.inflight) clearTimeout(this.inflight.timer)
		clearTimeout(this.presenceTimer)
		this.carets.destroy()
	}
}
