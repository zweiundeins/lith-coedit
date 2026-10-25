// A fake of the server's co-edited document, as the README describes it: the
// writer orders commits, rebases each over the commits since its base
// (rebase.js), keeps every version, and each editing tab's view renders the
// commits after the version the tab acknowledged. Used by the Node tests, and
// by an app's browser harness (in a SharedWorker).
import { Node } from 'prosemirror-model'
import { Step } from 'prosemirror-transform'
import { applyCommit } from './rebase.js'

// client is the first 12 hex digits of a hash of the tab id. The server uses
// SHA-256; the fake uses FNV-1a twice, which is synchronous.
export function clientOf(tab) {
	let h1 = 0x811c9dc5, h2 = 0x01000193
	for (let i = 0; i < tab.length; i++) {
		h1 = Math.imul(h1 ^ tab.charCodeAt(i), 0x01000193) >>> 0
		h2 = Math.imul(h2 ^ tab.charCodeAt(i), 0x811c9dc5) >>> 0
	}
	return 'c_' + (h1.toString(16).padStart(8, '0') + h2.toString(16).padStart(8, '0')).slice(0, 12)
}

const newEpoch = () => 'e_' + Math.random().toString(36).slice(2, 10)

export class Authority {
	// attrs are the app's own attributes of the host, added to every frame
	// (a value that is not a string as JSON).
	constructor({ schema, doc, page = 'p_demo', attrs = {} }) {
		this.schema = schema
		this.page = page
		this.attrs = attrs
		this.seed = doc
		this.tabs = new Map()
		this.stats = { commits: 0, deduped: 0, refused: 0, stepsIn: 0, stepsApplied: 0, presence: 0 }
		this.reset(doc, false)
	}

	// reset starts the document anew: a new epoch, version 1, no tab rows.
	reset(doc = this.seed, dropTabs = true) {
		this.epoch = newEpoch()
		this.doc = Node.fromJSON(this.schema, doc)
		this.version = 1
		this.floor = 1
		this.commits = [] // {v, ref, client, steps (JSON), parsed}
		this.refs = new Map() // ref -> version, for writer.Once
		if (dropTabs) for (const t of this.tabs.values()) t.row = null
		this.touch()
	}

	// open is the page's GET plus the open action: the document at the head
	// and the tab's row.
	open(tab, { name = 'Someone', hue = 0 } = {}) {
		const client = clientOf(tab)
		const row = { epoch: this.epoch, ack: this.version, v: this.version, anchor: 0, head: 0, seq: -1, refusal: '', seen: Date.now(), moved: Date.now() }
		const t = { tab, client, name, hue, base: this.version, row, dirty: true }
		this.tabs.set(tab, t)
		this.touch()
		return { page: this.page, epoch: this.epoch, client, base: this.version, doc: this.doc.toJSON() }
	}

	close(tab) {
		this.tabs.delete(tab)
		this.touch()
	}

	touch() {
		for (const t of this.tabs.values()) t.dirty = true
	}

	// commit is the commit action. It answers an HTTP status.
	commit({ tab, epoch, base, ref, steps }) {
		const t = this.tabs.get(tab)
		if (!t?.row || epoch !== this.epoch) return 409
		if (this.refs.has(ref)) {
			this.stats.deduped++
			return 204
		}
		if (base < this.floor) return 409
		if (base > this.version || !Array.isArray(steps) || steps.length > 500) return 422
		let parsed
		try {
			parsed = steps.map((j) => Step.fromJSON(this.schema, j))
		} catch {
			return 422
		}
		const since = this.commits.filter((c) => c.v > base).flatMap((c) => c.parsed)
		const out = applyCommit(this.doc, since, parsed)
		try {
			out.doc.check()
		} catch (e) {
			t.row.refusal = JSON.stringify({ ref, message: 'This change does not fit the document: ' + e.message })
			this.stats.refused++
			this.touch()
			return 422
		}
		if (this.refuse?.(out.doc, ref)) {
			t.row.refusal = JSON.stringify({ ref, message: this.refuse(out.doc, ref) })
			this.stats.refused++
			this.touch()
			return 422
		}
		this.doc = out.doc
		this.version++
		this.commits.push({ v: this.version, ref, client: t.client, steps: out.steps.map((s) => s.toJSON()), parsed: out.steps })
		this.refs.set(ref, this.version)
		this.stats.commits++
		this.stats.stepsIn += steps.length
		this.stats.stepsApplied += out.steps.length
		this.touch()
		return 204
	}

	// presence is the presence action; seq makes an overtaken post
	// change nothing.
	presence({ tab, epoch, ack, v, anchor, head, seq }) {
		const t = this.tabs.get(tab)
		if (!t?.row || epoch !== this.epoch) return 409
		if (seq <= t.row.seq) return 204
		this.stats.presence++
		const moved = anchor !== t.row.anchor || head !== t.row.head || v !== t.row.v
		Object.assign(t.row, { ack: Math.max(t.row.ack, Math.min(ack, this.version)), v, anchor, head, seq, seen: Date.now() })
		if (moved) t.row.moved = Date.now()
		this.touch()
		return 204
	}

	// fold forgets commits up to upto; older bases are a gap.
	fold(upto) {
		this.floor = Math.min(upto, this.version)
		this.commits = this.commits.filter((c) => c.v > this.floor)
		this.touch()
	}

	// frame is what the tab's view renders: the host's attributes.
	frame(tab) {
		const t = this.tabs.get(tab)
		if (!t) return null
		t.dirty = false
		let state = 'ok'
		const from = t.row ? Math.max(t.row.ack, t.base) : t.base
		if (!t.row || t.row.epoch !== this.epoch) state = 'reset'
		else if (from < this.floor) state = 'gap'
		// A test sets t.force to render gone or readonly for this tab.
		if (t.force && state === 'ok') state = t.force
		const batches =
			state === 'ok' || state === 'readonly'
				? this.commits
						.filter((c) => c.v > from)
						.slice(0, 500)
						.map(({ v, ref, client, steps }) => ({ v, ref, client, steps }))
				: []
		const now = Date.now()
		const carets = []
		for (const o of this.tabs.values()) {
			if (o === t || !o.row || o.row.seq < 0) continue
			carets.push({ id: o.client, name: o.name, hue: o.hue, v: o.row.v, anchor: o.row.anchor, head: o.row.head, idle: now - o.row.moved > 30000 })
		}
		return {
			id: 'editor',
			page: this.page,
			epoch: t.row?.epoch ?? this.epoch,
			client: t.client,
			base: String(t.base),
			head: String(this.version),
			batches: JSON.stringify(batches),
			carets: JSON.stringify(carets),
			...Object.fromEntries(Object.entries(this.attrs).map(([k, v]) => [k, typeof v === 'string' ? v : JSON.stringify(v)])),
			state,
			refusal: t.row?.refusal ?? '',
		}
	}
}
