import { test } from 'node:test'
import assert from 'node:assert/strict'
import { Schema } from 'prosemirror-model'
import { schemaSpec } from '../../src/schema.js'

test('schemaSpec keeps the order and adds the browser\'s parts', () => {
	const json = { topNode: 'doc', nodes: [{ name: 'doc', content: 'block+' }, { name: 'paragraph', group: 'block', content: 'inline*' }, { name: 'text', group: 'inline' }], marks: [{ name: 'em' }, { name: 'strong' }], limits: {} }
	const toDOM = () => ['p', 0]
	const s = new Schema(schemaSpec(json, { nodes: { paragraph: { toDOM } } }))
	assert.deepEqual(Object.keys(s.nodes), ['doc', 'paragraph', 'text'])
	assert.deepEqual(Object.keys(s.marks), ['em', 'strong'])
	assert.equal(s.nodes.paragraph.spec.toDOM, toDOM)
	assert.equal(s.topNodeType.name, 'doc')
})
