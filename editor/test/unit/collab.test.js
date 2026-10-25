// The commit protocol without a browser: editors as EditorStates against the
// fake authority, over a network that delays, reorders posts and skips
// frames: no edit lost, all equal, undo of own steps only, skipped frames.
import { test } from 'node:test'
import assert from 'node:assert/strict'
import { EditorState, TextSelection } from 'prosemirror-state'
import { history, undo } from 'prosemirror-history'
import { schema } from '../schema.js'
import { collab, makeCommit, batchTransaction, getVersion, collabState, receiveTransaction } from '../../src/collab.js'
import { Authority } from '../authority/authority.js'

const seedDoc = { type: 'doc', content: [{ type: 'paragraph', content: [{ type: 'text', text: 'Hello world' }] }] }

// A seeded random number generator (mulberry32).
function rng(seed) {
	return () => {
		seed |= 0
		seed = (seed + 0x6d2b79f5) | 0
		let t = Math.imul(seed ^ (seed >>> 15), 1 | seed)
		t = (t + Math.imul(t ^ (t >>> 7), 61 | t)) ^ t
		return ((t ^ (t >>> 14)) >>> 0) / 4294967296
	}
}

class Client {
	constructor(auth, tab, rand) {
		this.auth = auth
		this.tab = tab
		this.rand = rand
		const o = auth.open(tab)
		this.epoch = o.epoch
		this.state = EditorState.create({ doc: schema.nodeFromJSON(o.doc), plugins: [collab({ version: o.base }), history()] })
		this.inflight = null
		this.stats = { reconciled: 0, own: 0 }
		this.typed = ''
	}
	apply(tr) {
		this.state = this.state.apply(tr)
	}
	edit() {
		const { state, rand } = this
		const size = state.doc.content.size
		const r = rand()
		const pos = 1 + Math.floor(rand() * Math.max(1, size - 1))
		let tr = state.tr
		try {
			if (r < 0.7) {
				const ch = this.alphabet[Math.floor(rand() * this.alphabet.length)]
				tr.setSelection(TextSelection.near(state.doc.resolve(Math.min(pos, size - 1))))
				tr.insertText(ch)
				this.typed += ch
			} else if (r < 0.8 && size > 4) {
				const from = Math.min(pos, size - 2)
				tr.delete(from, Math.min(from + 1 + Math.floor(rand() * 3), size - 1))
				this.deletes = true
			} else if (r < 0.88) {
				tr.addMark(Math.min(pos, size - 1), Math.min(pos + 3, size - 1), schema.marks.strong.create())
			} else if (r < 0.94) {
				tr.split(Math.min(pos, size - 1))
			} else {
				tr.setSelection(TextSelection.near(state.doc.resolve(Math.min(pos, size - 1))))
				const $f = tr.selection.$from
				if ($f.parent.type === schema.nodes.paragraph) tr.setBlockType($f.before(), $f.after(), schema.nodes.heading, { level: 2 })
			}
		} catch {
			return
		}
		if (tr.docChanged) this.apply(tr)
	}
	sendable() {
		if (this.inflight) return null
		this.inflight = makeCommit(this.state)
		return this.inflight
	}
	frame(f) {
		const batches = JSON.parse(f.batches)
		for (const b of batches) {
			const v = getVersion(this.state)
			if (b.v <= v) continue
			if (b.v !== v + 1) break
			const r = batchTransaction(this.state, schema, b, this.inflight)
			assert.ok(r.tr, 'commit applies')
			if (r.own) {
				this.inflight = null
				this.stats.own++
				if (r.reconciled) this.stats.reconciled++
			}
			this.apply(r.tr)
		}
		return getVersion(this.state)
	}
}

function run(seed, { clients = 5, rounds = 400, skip = 0.3, alphabets } = {}) {
	const rand = rng(seed)
	const auth = new Authority({ schema, doc: seedDoc })
	const cs = Array.from({ length: clients }, (_, i) => new Client(auth, 't' + i, rng(seed * 31 + i)))
	cs.forEach((c, i) => (c.alphabet = alphabets?.[i] ?? 'abcdefghij'[i % 10]))
	const posts = [] // [deliverAt, client, commit]
	const acks = [] // [deliverAt, client, version]
	for (let tick = 0; tick < rounds + 200; tick++) {
		if (tick < rounds) for (const c of cs) if (rand() < 0.5) c.edit()
		for (const c of cs) {
			const k = c.sendable()
			if (k) posts.push([tick + 1 + Math.floor(rand() * 3), c, k])
		}
		// Posts arrive in any order after 1 to 3 ticks.
		for (let i = 0; i < posts.length; ) {
			if (posts[i][0] <= tick) {
				const [, c, k] = posts.splice(i, 1)[0]
				assert.equal(auth.commit({ tab: c.tab, epoch: c.epoch, base: k.base, ref: k.ref, steps: k.steps }), 204)
			} else i++
		}
		for (let i = 0; i < acks.length; ) {
			if (acks[i][0] <= tick) {
				const [, c, v] = acks.splice(i, 1)[0]
				auth.presence({ tab: c.tab, epoch: c.epoch, ack: v, v, anchor: 0, head: 0, seq: tick })
			} else i++
		}
		for (const c of cs) {
			if (rand() < skip) continue // a skipped frame
			const v = c.frame(auth.frame(c.tab))
			acks.push([tick + 1 + Math.floor(rand() * 2), c, v])
		}
	}
	return { auth, cs }
}

for (const seed of [1, 2, 3, 4, 5, 6, 7, 8]) {
	test(`five editors converge, seed ${seed}`, () => {
		const { auth, cs } = run(seed)
		const want = JSON.stringify(auth.doc.toJSON())
		for (const c of cs) {
			assert.equal(collabState(c.state).unconfirmed.length, 0, 'all confirmed')
			assert.equal(JSON.stringify(c.state.doc.toJSON()), want, c.tab + ' equals the writer')
		}
		const reconciled = cs.reduce((n, c) => n + c.stats.reconciled, 0)
		const own = cs.reduce((n, c) => n + c.stats.own, 0)
		assert.ok(own > 50)
		// The writer and the editors rebase alike: our own commits only confirm.
		if (process.env.SHOW) console.log(`seed ${seed}: own ${own}, reconciled ${reconciled}, writer dropped ${auth.stats.stepsIn - auth.stats.stepsApplied} of ${auth.stats.stepsIn}`)
		assert.ok(reconciled <= own * 0.02, `reconciled ${reconciled} of ${own}`)
	})
}

test('typing only: every character survives, in each editor order', () => {
	const alphabets = ['q', 'v', 'x', 'z', 'j']
	const rand = rng(99)
	const auth = new Authority({ schema, doc: seedDoc })
	const cs = alphabets.map((a, i) => Object.assign(new Client(auth, 't' + i, rng(i + 7)), { alphabet: a }))
	const posts = []
	for (let tick = 0; tick < 600; tick++) {
		if (tick < 400)
			for (const c of cs) {
				// Everyone types at the end of the first paragraph's text, in turn.
				const end = c.state.doc.firstChild.nodeSize - 1
				const tr = c.state.tr.setSelection(TextSelection.create(c.state.doc, end)).insertText(c.alphabet)
				c.typed += c.alphabet
				c.apply(tr)
			}
		for (const c of cs) {
			const k = c.sendable()
			if (k) posts.push([tick + 1 + Math.floor(rand() * 4), c, k])
		}
		for (let i = 0; i < posts.length; ) {
			if (posts[i][0] <= tick) {
				const [, c, k] = posts.splice(i, 1)[0]
				auth.commit({ tab: c.tab, epoch: c.epoch, base: k.base, ref: k.ref, steps: k.steps })
			} else i++
		}
		for (const c of cs) {
			if (rand() < 0.3) continue
			const v = c.frame(auth.frame(c.tab))
			auth.presence({ tab: c.tab, epoch: c.epoch, ack: v, v, anchor: 0, head: 0, seq: tick })
		}
	}
	const text = auth.doc.textContent
	for (const c of cs) {
		assert.equal(c.state.doc.textContent, text)
		assert.equal(text.split(c.alphabet).length - 1, 400)
	}
})

test('a resent commit applies once', () => {
	const auth = new Authority({ schema, doc: seedDoc })
	const a = new Client(auth, 'a', rng(1))
	a.apply(a.state.tr.insertText('xyz', 1))
	const k = a.sendable()
	const body = { tab: 'a', epoch: a.epoch, base: k.base, ref: k.ref, steps: k.steps }
	assert.equal(auth.commit(body), 204)
	assert.equal(auth.commit(body), 204)
	assert.equal(auth.stats.deduped, 1)
	a.frame(auth.frame('a'))
	assert.equal(auth.doc.textContent, 'xyzHello world')
	assert.equal(a.state.doc.textContent, 'xyzHello world')
	assert.equal(collabState(a.state).unconfirmed.length, 0)
})

test('undo takes back only the own steps, rebased', () => {
	const auth = new Authority({ schema, doc: seedDoc })
	const a = new Client(auth, 'a', rng(1))
	const b = new Client(auth, 'b', rng(2))
	a.apply(a.state.tr.insertText('AAA', 1))
	b.apply(b.state.tr.insertText('BBB', 12))
	for (const c of [a, b]) {
		const k = c.sendable()
		auth.commit({ tab: c.tab, epoch: c.epoch, base: k.base, ref: k.ref, steps: k.steps })
	}
	a.frame(auth.frame('a'))
	b.frame(auth.frame('b'))
	assert.equal(a.state.doc.textContent, 'AAAHello worldBBB')
	undo(a.state, (tr) => a.apply(tr))
	assert.equal(a.state.doc.textContent, 'Hello worldBBB')
	const k = a.sendable()
	auth.commit({ tab: 'a', epoch: a.epoch, base: k.base, ref: k.ref, steps: k.steps })
	a.frame(auth.frame('a'))
	b.frame(auth.frame('b'))
	assert.equal(auth.doc.textContent, 'Hello worldBBB')
	assert.equal(b.state.doc.textContent, 'Hello worldBBB')
})

test('a refused commit is taken out, later steps stay', () => {
	const auth = new Authority({ schema, doc: seedDoc })
	const a = new Client(auth, 'a', rng(1))
	a.apply(a.state.tr.insertText('bad', 1))
	const k = a.sendable()
	a.apply(a.state.tr.insertText('good', a.state.doc.content.size - 1))
	const tr = receiveTransaction(a.state, [], { drop: k.last })
	a.apply(tr)
	a.inflight = null
	assert.equal(a.state.doc.textContent, 'Hello worldgood')
	assert.equal(collabState(a.state).unconfirmed.length, 1)
})

test('a commit that differs from ours reconciles to the writer (divergence guard)', () => {
	const auth = new Authority({ schema, doc: seedDoc })
	const a = new Client(auth, 'a', rng(1))
	a.apply(a.state.tr.insertText('mine', 1))
	const k = a.sendable()
	a.apply(a.state.tr.insertText('later', a.state.doc.content.size - 1))
	// The writer applies something else under our ref (as a buggy port might).
	const other = a.state.schema.text('MINE')
	const step = { stepType: 'replace', from: 1, to: 1, slice: { content: [other.toJSON()] } }
	const r = batchTransaction(a.state, schema, { v: 2, ref: k.ref, steps: [step] }, k)
	assert.ok(r.reconciled)
	a.apply(r.tr)
	assert.equal(a.state.doc.textContent, 'MINEHello worldlater')
	assert.equal(collabState(a.state).unconfirmed.length, 1)
})
