// The schema of the Node tests: paragraphs, headings, text and strong, and
// two node types with stable ids: photo (an atom block) and day (a heading).
import { Schema } from 'prosemirror-model'

export const schema = new Schema({
	nodes: {
		doc: { content: 'block+' },
		paragraph: { group: 'block', content: 'inline*' },
		heading: { group: 'block', content: 'inline*', attrs: { level: { default: 1 } } },
		photo: { group: 'block', atom: true, attrs: { id: { default: '' }, src: { default: '' } } },
		day: { group: 'block', content: 'inline*', attrs: { id: { default: '' } } },
		text: { group: 'inline' },
	},
	marks: { strong: {} },
})
