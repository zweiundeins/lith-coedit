// The Session against the fake authority, without a browser: a fake view
// holds the EditorState, frames come as the host's attributes, posts go
// to the authority after a delay.
import { test } from 'node:test'
import assert from 'node:assert/strict'
import { EditorState, TextSelection } from 'prosemirror-state'
import { schema } from '../schema.js'
import { collab } from '../../src/collab.js'
import { caretPlugin } from '../../src/carets.js'
import { commentPlugin } from '../../src/comments.js'
import { Session } from '../../src/session.js'
import { Authority } from '../authority/authority.js'

const seedDoc = { type: 'doc', content: [{ type: 'paragraph', content: [{ type: 'text', text: 'Hello world' }] }] }
const tick = (ms = 5) => new Promise((r) => setTimeout(r, ms))

// A tab: a fake EditorView, the host's attributes, and the page's wiring.
class Tab {
	constructor(auth, tab) {
		this.auth = auth
		this.tab = tab
		this.events = []
		const o = auth.open(tab)
		this.attrs = { epoch: o.epoch, client: o.client, tab, state: 'ok', batches: '[]', carets: '[]', comments: '', refusal: '' }
		const state = EditorState.create({ doc: schema.nodeFromJSON(o.doc), plugins: [collab({ version: o.base }), caretPlugin(), commentPlugin()] })
		const self = this
		this.view = {
			state,
			dispatch(tr) {
				this.state = this.state.apply(tr)
				self.session?.transaction(tr)
			},
			setProps() {},
			hasFocus: () => false,
		}
		this.session = new Session({
			view: this.view,
			schema,
			read: () => this.attrs,
			presenceMs: 0,
			confirmWait: 20,
			emit: (type, detail) => {
				this.events.push([type, detail])
				if (type === 'commit') setTimeout(() => this.session.posted(detail.ref, true, auth.commit({ ...detail, tab })), 1)
				if (type === 'presence') auth.presence({ ...detail, tab })
			},
		})
	}
	type(text) {
		const { state } = this.view
		const end = state.doc.firstChild.nodeSize - 1
		this.view.dispatch(state.tr.setSelection(TextSelection.create(state.doc, end)).insertText(text))
	}
	frame() {
		const f = this.auth.frame(this.tab)
		if (!f) return
		Object.assign(this.attrs, { epoch: f.epoch, state: f.state, batches: f.batches, carets: f.carets, refusal: f.refusal })
		this.session.sync()
	}
	types(type) {
		return this.events.filter(([t]) => t === type).map(([, d]) => d)
	}
}

async function settle(tabs) {
	for (let i = 0; i < 60; i++) {
		await tick()
		for (const t of tabs) t.frame()
	}
}

test('three sessions type at once and converge on the writer', async () => {
	const auth = new Authority({ schema, doc: seedDoc })
	const tabs = ['a', 'b', 'c'].map((t) => new Tab(auth, t))
	for (let i = 0; i < 20; i++) {
		for (const t of tabs) t.type(t.tab)
		await tick(2)
		if (i % 3 === 0) for (const t of tabs) t.frame()
	}
	await settle(tabs)
	const want = auth.doc.textContent
	for (const t of tabs) {
		assert.equal(t.view.state.doc.textContent, want)
		assert.equal(t.session.unsaved(), 0)
		assert.equal(want.split(t.tab).length - 1, 20)
		assert.ok(t.types('ack').length > 0)
		assert.ok(t.types('presence').length > 0)
	}
	assert.equal(auth.stats.deduped, 0)
})

test('a refusal takes the commit out and says why', async () => {
	const auth = new Authority({ schema, doc: seedDoc })
	auth.refuse = (doc) => (doc.textContent.includes('bad') ? 'Not that word.' : '')
	const a = new Tab(auth, 'a')
	a.type('bad')
	await settle([a])
	assert.deepEqual(a.types('refused'), [{ ref: a.types('commit')[0].ref, message: 'Not that word.', dropped: 1 }])
	assert.equal(a.view.state.doc.textContent, 'Hello world')
	assert.equal(a.session.unsaved(), 0)
})

test('a reset stops the session and keeps the unsaved text', async () => {
	const auth = new Authority({ schema, doc: seedDoc })
	const a = new Tab(auth, 'a')
	a.type('x')
	auth.reset()
	a.type('y')
	await settle([a])
	assert.equal(a.session.stopped, 'reset')
	assert.equal(a.types('stopped')[0].state, 'reset')
	assert.ok(a.view.state.doc.textContent.endsWith('y'))
	assert.equal(a.session.editable(), false)
})

test('readonly applies frames, sends nothing, and resumes', async () => {
	const auth = new Authority({ schema, doc: seedDoc })
	const a = new Tab(auth, 'a'), b = new Tab(auth, 'b')
	auth.tabs.get('a').force = 'readonly'
	auth.touch()
	a.frame()
	a.type('mine')
	b.type('theirs')
	await settle([a, b])
	assert.equal(a.session.stopped, 'readonly')
	assert.ok(a.view.state.doc.textContent.includes('theirs'))
	assert.equal(a.types('commit').length, 0)
	auth.tabs.get('a').force = null
	auth.touch()
	await settle([a, b])
	assert.equal(a.types('resumed').length, 1)
	assert.equal(a.session.unsaved(), 0)
	assert.ok(auth.doc.textContent.includes('mine'))
	for (const t of [a, b]) t.session.destroy()
})
