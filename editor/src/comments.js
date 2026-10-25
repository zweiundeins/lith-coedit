// The highlights of inline comments. The host's comments attribute is
// {v, list: [{id, from, to}]}, the ranges at version v of the
// document, from the same frame as the batches. Each range maps through the
// confirmed commits after v (the step maps the carets keep) and our
// unconfirmed steps, as carets do; between frames the decorations map
// through every transaction, with the server's rule: text typed at either
// edge stays outside.
//
// DOM: <mark class="highlight" data-highlight="<comment id>"> around the
// text (drawComments' className and attr), as prosemirror-go's
// RenderHighlighted writes the reader's HTML, so the page's style and
// clicks serve both.
import { Plugin, PluginKey } from 'prosemirror-state'
import { Decoration, DecorationSet } from 'prosemirror-view'
import { Mapping } from 'prosemirror-transform'
import { collabState } from './collab.js'

export const commentKey = new PluginKey('comments')

const ID = /^[A-Za-z0-9_-]{1,64}$/

export function commentPlugin() {
	return new Plugin({
		key: commentKey,
		state: {
			init: () => DecorationSet.empty,
			apply(tr, set) {
				const next = tr.getMeta(commentKey)
				return next ?? set.map(tr.mapping, tr.doc)
			},
		},
		props: { decorations: (state) => commentKey.getState(state) },
	})
}

// since maps a position at version v onto the document on screen, or is
// null when the commits after v are not known (yet).
function since(maps, state, v) {
	const { version, unconfirmed } = collabState(state)
	if (!Number.isInteger(v) || v > version) return null
	const m = new Mapping()
	for (let i = v + 1; i <= version; i++) {
		const list = maps.get(i)
		if (!list) return null
		for (const map of list) m.appendMap(map)
	}
	for (const u of unconfirmed) m.appendMap(u.step.getMap())
	return m
}

// placeComments is where value's ranges are on the document on screen,
// and whether value's version was known: a range from a version not
// received yet keeps where it was drawn.
export function placeComments(state, maps, value) {
	const { doc } = state
	const list = value && Array.isArray(value.list) ? value.list : []
	const m = since(maps, state, value?.v)
	const prev = commentKey.getState(state) ?? DecorationSet.empty
	const out = []
	for (const c of list) {
		if (!c || !ID.test(String(c.id)) || !Number.isInteger(c.from) || !Number.isInteger(c.to)) continue
		let from, to
		if (m) {
			from = m.map(c.from, 1)
			to = m.map(c.to, -1)
		} else {
			const old = prev.find(undefined, undefined, (spec) => spec.comment === c.id)
			if (!old.length) continue
			from = Math.min(...old.map((d) => d.from))
			to = Math.max(...old.map((d) => d.to))
		}
		from = Math.max(0, from)
		to = Math.min(to, doc.content.size)
		if (from < to) out.push({ id: c.id, from, to })
	}
	return { ranges: out, known: !!m }
}

// drawComments draws value's ranges from scratch; false when they were
// from a version not received yet.
export function drawComments(view, maps, value, { className = 'highlight', attr = 'data-highlight' } = {}) {
	const { ranges, known } = placeComments(view.state, maps, value)
	const decos = ranges.map((r) => Decoration.inline(r.from, r.to, { nodeName: 'mark', class: className, [attr]: r.id }, { comment: r.id, inclusiveEnd: true }))
	view.dispatch(view.state.tr.setMeta(commentKey, DecorationSet.create(view.state.doc, decos)).setMeta('addToHistory', false))
	return known
}

// commentRange is the selection as an inline comment's range: at the
// confirmed version, mapped back through our unconfirmed steps, with the
// characters of its text nodes (the server compares them); null without a
// selection of text.
export function commentRange(state) {
	const { from, to } = state.selection
	if (from >= to) return null
	const { version, unconfirmed } = collabState(state)
	const back = new Mapping(unconfirmed.map((u) => u.step.getMap())).invert()
	const f = back.map(from, 1), t = back.map(to, -1)
	const text = state.doc.textBetween(from, to, '', () => '')
	if (f >= t || !text) return null
	return { v: version, from: f, to: t, text }
}
