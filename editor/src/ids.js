// Stable ids of nodes, as prosemirror-go's IDs names them: {image: 'id'}
// gives every image node an id attribute that stays through every edit, so
// a comment, a map or a link can name the node itself. The plugin gives a
// node of such a type an id when an edit of this editor makes it without
// one, and a new id to a copy (a paste, a split) whose id another node
// holds; the server's Rules.IDs refuses a commit that leaves either. Nodes
// that arrive in the server's commits are left alone.
import { Plugin, PluginKey } from 'prosemirror-state'
import { REMOTE } from './collab.js'

export const idsKey = new PluginKey('ids')

export function randomId() {
	const b = new Uint8Array(9)
	globalThis.crypto.getRandomValues(b)
	return Array.from(b, (x) => x.toString(36).padStart(2, '0')).join('').slice(0, 16)
}

// changedRanges are the ranges of the new document that local steps of trs
// wrote, mapped to the end.
function changedRanges(trs) {
	let ranges = []
	for (const tr of trs) {
		const local = !tr.getMeta(REMOTE)
		for (const step of tr.steps) {
			const map = step.getMap()
			ranges = ranges.map(([a, b]) => [map.map(a, -1), map.map(b, 1)])
			if (local) map.forEach((_os, _oe, ns, ne) => ranges.push([ns, ne]))
		}
	}
	return ranges
}

// idPlugin assigns ids. ids maps node type names to their id attribute;
// make returns a new id.
export function idPlugin({ ids, make = randomId }) {
	return new Plugin({
		key: idsKey,
		appendTransaction(trs, _old, state) {
			if (!trs.some((tr) => tr.docChanged && !tr.getMeta(REMOTE))) return null
			const ranges = changedRanges(trs)
			const inRange = []
			const seen = new Set()
			for (const [from, to] of ranges)
				state.doc.nodesBetween(Math.max(0, from), Math.min(to, state.doc.content.size), (node, pos) => {
					const attr = ids[node.type.name]
					if (attr && !seen.has(pos)) seen.add(pos), inRange.push({ node, pos, attr })
				})
			if (!inRange.length) return null
			const outside = new Set()
			state.doc.descendants((node, pos) => {
				const attr = ids[node.type.name]
				if (attr && !seen.has(pos) && node.attrs[attr]) outside.add(node.attrs[attr])
			})
			inRange.sort((a, b) => a.pos - b.pos)
			const tr = state.tr
			const taken = new Set()
			for (const { node, pos, attr } of inRange) {
				const id = node.attrs[attr]
				if (id && !outside.has(id) && !taken.has(id)) {
					taken.add(id)
					continue
				}
				let fresh = make()
				while (outside.has(fresh) || taken.has(fresh)) fresh = make()
				taken.add(fresh)
				tr.setNodeAttribute(pos, attr, fresh)
			}
			return tr.steps.length ? tr : null
		},
	})
}

// findNode returns the node with the id and its position, or null.
export function findNode(doc, ids, id) {
	let found = null
	if (!id) return null
	doc.descendants((node, pos) => {
		if (found) return false
		const attr = ids[node.type.name]
		if (attr && node.attrs[attr] === id) found = { node, pos }
		return !found
	})
	return found
}
