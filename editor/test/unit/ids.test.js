// Stable ids: new nodes get one, copies a new one, the server's nodes none.
import { test } from 'node:test'
import assert from 'node:assert/strict'
import { EditorState, NodeSelection } from 'prosemirror-state'
import { schema } from '../schema.js'
import { collab, collabState, REMOTE } from '../../src/collab.js'
import { idPlugin, findNode } from '../../src/ids.js'
import { commentPlugin, placeComments, commentRange, drawComments, commentKey } from '../../src/comments.js'

const ids = { photo: 'id', day: 'id' }
let n = 0
const make = () => 'n' + ++n

function start(doc) {
	return EditorState.create({ doc: schema.nodeFromJSON(doc), plugins: [collab({ version: 1 }), idPlugin({ ids, make }), commentPlugin()] })
}
const doc = { type: 'doc', content: [{ type: 'paragraph', content: [{ type: 'text', text: 'Hi' }] }, { type: 'photo', attrs: { id: 'p1' } }] }
const idsOf = (state) => {
	const out = []
	state.doc.descendants((node) => ids[node.type.name] && out.push(node.attrs.id))
	return out
}

test('a new node gets an id, a copy a new one, the original keeps its own', () => {
	n = 0
	let state = start(doc)
	state = state.apply(state.tr.insert(0, schema.nodes.photo.create()))
	assert.deepEqual(idsOf(state), ['n1', 'p1'])
	// A paste of the photo before itself: the copy is the new one.
	state = state.apply(state.tr.insert(0, state.doc.child(2).copy()))
	assert.deepEqual(idsOf(state), ['n2', 'n1', 'p1'])
	// The steps go out with the next commit.
	assert.ok(collabState(state).unconfirmed.length >= 4)
})

test('a split keeps the id in the first half', () => {
	n = 0
	let state = start({ type: 'doc', content: [{ type: 'day', attrs: { id: 'd1' }, content: [{ type: 'text', text: 'Tuesday' }] }] })
	state = state.apply(state.tr.split(4))
	assert.deepEqual(idsOf(state), ['d1', 'n1'])
})

test('nodes in the server\'s commits are left alone', () => {
	let state = start(doc)
	state = state.apply(state.tr.insert(0, schema.nodes.photo.create({ id: 'p1' })).setMeta(REMOTE, true))
	assert.deepEqual(idsOf(state), ['p1', 'p1'])
})

test('a comment anchors to a node by id and goes with it', () => {
	let state = start(doc)
	const value = { v: 1, list: [{ id: 'c1', node: 'p1' }, { id: 'c2', node: 'gone' }, { id: 'c3', from: 1, to: 3 }] }
	const at = placeComments(state, new Map(), value, ids)
	assert.deepEqual(at.ranges, [{ id: 'c1', node: 'p1', from: 4, to: 5 }, { id: 'c3', from: 1, to: 3 }])
	assert.deepEqual(findNode(state.doc, ids, 'p1').pos, 4)
	state = state.apply(state.tr.setSelection(NodeSelection.create(state.doc, 4)))
	assert.deepEqual(commentRange(state, ids), { v: 1, node: 'p1' })
	// Drawn as a node decoration with the class and the attribute.
	const view = { state, dispatch(tr) { this.state = this.state.apply(tr) } }
	drawComments(view, new Map(), value, { className: 'hl', attr: 'data-c', ids })
	const decos = commentKey.getState(view.state).find()
	const node = decos.find((d) => d.from === 4)
	assert.equal(node.type.attrs.class, 'hl')
	assert.equal(node.type.attrs['data-c'], 'c1')
	view.state = view.state.apply(view.state.tr.delete(4, 5))
	assert.equal(commentKey.getState(view.state).find().filter((d) => d.spec.comment === 'c1').length, 0)
})
