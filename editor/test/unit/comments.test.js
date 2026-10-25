// Inline comments' ranges in the editor, without a browser.
import { test } from 'node:test'
import assert from 'node:assert/strict'
import { EditorState, TextSelection } from 'prosemirror-state'
import { schema } from '../schema.js'
import { collab, batchTransaction, getVersion } from '../../src/collab.js'
import { commentPlugin, placeComments, commentRange } from '../../src/comments.js'

const id = '0192d000-0000-7000-8000-000000000001'
const doc = { type: 'doc', content: [{ type: 'paragraph', content: [{ type: 'text', text: 'Say hello now' }] }] }

function start(version = 3) {
	return EditorState.create({ doc: schema.nodeFromJSON(doc), plugins: [collab({ version }), commentPlugin()] })
}

const insert = (pos, text) => ({ stepType: 'replace', from: pos, to: pos, slice: { content: [{ type: 'text', text }] } })

test('a range at the confirmed version stays on its text through local and confirmed edits', () => {
	let state = start()
	const maps = new Map()
	// A commit of someone else's before the word: version 4.
	const b = { v: 4, ref: 'x', client: 'c', steps: [insert(1, 'Oh, ')] }
	const r = batchTransaction(state, schema, b, null)
	maps.set(4, r.steps.map((s) => s.getMap()))
	state = state.apply(r.tr)
	// Our own unconfirmed typing at the word's start and end.
	state = state.apply(state.tr.insertText('X', 9).insertText('Y', 15))
	const at = placeComments(state, maps, { v: 3, list: [{ id, from: 5, to: 10 }] })
	assert.equal(at.known, true)
	assert.equal(state.doc.textBetween(at.ranges[0].from, at.ranges[0].to), 'hello')
	// From a version not received yet: nothing drawn before, nothing now.
	assert.deepEqual(placeComments(state, maps, { v: 9, list: [{ id, from: 5, to: 10 }] }), { ranges: [], known: false })
	// Malformed entries are left out.
	assert.deepEqual(placeComments(state, maps, { v: 4, list: [{ id: 'x y', from: 1, to: 2 }, { id, from: 'a', to: 2 }, null] }).ranges, [])
})

test('the selection maps back to the confirmed version with its text', () => {
	let state = start()
	state = state.apply(state.tr.insertText('Well ', 1)) // unconfirmed
	const from = 10, to = 15 // "hello" after "Well Say "
	state = state.apply(state.tr.setSelection(TextSelection.create(state.doc, from, to)))
	assert.deepEqual(commentRange(state), { v: 3, from: 5, to: 10, text: 'hello' })
	state = state.apply(state.tr.setSelection(TextSelection.create(state.doc, 3)))
	assert.equal(commentRange(state), null)
	assert.equal(getVersion(state), 3)
})
