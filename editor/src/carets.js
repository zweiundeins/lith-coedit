// Other people's carets and selections. Each caret arrives at the
// version v its sender had confirmed; it maps through the confirmed commits
// after v (their step maps are kept for the last 500 versions), then through
// our unconfirmed steps, onto the document on screen. Between frames the
// decorations map through every transaction, so a caret keeps its place in
// the text while people type around it.
//
// DOM, with the class prefix p of the Carets options ('coedit' by default):
//   <span class="p-caret" data-hue="7" aria-hidden="true"><span class="p-caret__tag">Sunny Otter</span></span>
//   .p-caret--tag   while the name shows: 3 s after the caret moved, or near our caret
//   .p-caret--idle  the person has not moved for 30 s (no name, even on hover)
//   <span class="p-remote-sel" data-hue="7"> around the selected text
import { Plugin, PluginKey, Selection } from 'prosemirror-state'
import { Decoration, DecorationSet } from 'prosemirror-view'
import { Mapping } from 'prosemirror-transform'
import { collabState } from './collab.js'

export const caretKey = new PluginKey('carets')
const KEEP = 500
const SHOW_MS = 3000

export function caretPlugin() {
	return new Plugin({
		key: caretKey,
		state: {
			init: () => DecorationSet.empty,
			apply(tr, set) {
				const next = tr.getMeta(caretKey)
				return next ?? set.map(tr.mapping, tr.doc)
			},
		},
		props: { decorations: (state) => caretKey.getState(state) },
	})
}

// Carets keeps the step maps of confirmed commits and draws the carets.
export class Carets {
	constructor(view, { prefix = 'coedit' } = {}) {
		this.view = view
		this.cls = { caret: prefix + '-caret', tag: prefix + '-caret__tag', shown: prefix + '-caret--tag', idle: prefix + '-caret--idle', sel: prefix + '-remote-sel' }
		this.maps = new Map() // version -> StepMap[]
		this.list = []
		this.last = new Map() // id -> {v, anchor, head, at, pos} of the last drawn caret
		this.els = new Map() // id -> caret element
		this.timer = 0
	}

	// confirmed records the steps of the commit that made version v.
	confirmed(v, steps) {
		this.maps.set(v, steps.map((s) => s.getMap()))
		this.maps.delete(v - KEEP)
	}

	// toNow maps a position at version v onto the document on screen, or
	// returns null when the steps since v are not known (yet).
	toNow(v, pos) {
		const state = this.view.state
		const now = collabState(state).version
		if (v > now) return null
		const m = new Mapping()
		for (let i = v + 1; i <= now; i++) {
			const maps = this.maps.get(i)
			if (!maps) return null
			for (const map of maps) m.appendMap(map)
		}
		for (const u of collabState(state).unconfirmed) m.appendMap(u.step.getMap())
		return Math.max(0, Math.min(m.map(pos), state.doc.content.size))
	}

	set(list, self) {
		this.list = Array.isArray(list) ? list.filter((c) => c && c.id !== self && typeof c.anchor === 'number' && typeof c.head === 'number') : []
		this.draw()
	}

	// draw puts every caret where its source says, from scratch.
	draw() {
		const { view } = this
		const { doc } = view.state
		const now = Date.now()
		const decos = []
		const seen = new Set()
		const prev = caretKey.getState(view.state)
		for (const c of this.list) {
			const id = String(c.id)
			seen.add(id)
			const was = this.last.get(id)
			const moved = !was || was.v !== c.v || was.anchor !== c.anchor || was.head !== c.head
			let anchor = this.toNow(c.v, c.anchor)
			let head = this.toNow(c.v, c.head)
			if (anchor == null || head == null) {
				// From a version we have not received: keep where it was drawn.
				const old = prev.find(undefined, undefined, (spec) => spec.caret === id)[0]
				if (!old) continue
				anchor = head = old.from
			}
			head = textPos(doc, head)
			const at = moved ? now : (was?.at ?? 0)
			this.last.set(id, { v: c.v, anchor: c.anchor, head: c.head, at })
			const hue = String(Number.parseInt(c.hue, 10) & 15)
			const name = String(c.name ?? '')
			const from = Math.min(anchor, head), to = Math.max(anchor, head)
			if (from < to) decos.push(Decoration.inline(from, to, { class: this.cls.sel, 'data-hue': hue }, { sel: id }))
			const idle = !!c.idle
			decos.push(
				Decoration.widget(head, () => this.caretDOM(id, name, hue, idle), {
					key: `caret:${id}:${hue}:${name}:${idle}`,
					caret: id,
					side: 1,
					marks: [],
					ignoreSelection: true,
				}),
			)
		}
		for (const id of this.last.keys()) if (!seen.has(id)) this.last.delete(id), this.els.delete(id)
		const set = DecorationSet.create(doc, decos)
		view.dispatch(view.state.tr.setMeta(caretKey, set).setMeta('addToHistory', false))
		this.tags()
	}

	caretDOM(id, name, hue, idle) {
		const el = document.createElement('span')
		el.className = this.cls.caret + (idle ? ' ' + this.cls.idle : '')
		el.dataset.hue = hue
		el.setAttribute('aria-hidden', 'true')
		const tag = document.createElement('span')
		tag.className = this.cls.tag
		tag.textContent = name
		el.append(tag)
		this.els.set(id, el)
		return el
	}

	// tags shows a name for 3 s after its caret moved, and while it is within
	// two lines of our own caret.
	tags() {
		clearTimeout(this.timer)
		const { view } = this
		const now = Date.now()
		let mine = null
		try {
			if (view.hasFocus()) mine = view.coordsAtPos(view.state.selection.head)
		} catch {}
		let next = Infinity
		for (const [id, el] of this.els) {
			if (!el.isConnected) continue
			const at = this.last.get(id)?.at ?? 0
			let show = !el.classList.contains(this.cls.idle) && now - at < SHOW_MS
			if (show) next = Math.min(next, at + SHOW_MS - now)
			if (!show && mine && !el.classList.contains(this.cls.idle)) {
				const r = el.getBoundingClientRect()
				const line = r.height || 20
				show = Math.abs(r.top - mine.top) < line * 2.5
			}
			el.classList.toggle(this.cls.shown, show)
		}
		if (next < Infinity) this.timer = setTimeout(() => this.tags(), next + 20)
	}

	destroy() {
		clearTimeout(this.timer)
	}
}

// A caret sits in text: a position between blocks moves into the nearest one.
function textPos(doc, pos) {
	const $pos = doc.resolve(pos)
	if ($pos.parent.inlineContent) return pos
	const sel = Selection.near($pos)
	return sel.head ?? sel.from
}
