// The schema of the Node tests: paragraphs, headings, text and strong.
import { Schema } from 'prosemirror-model'

export const schema = new Schema({
	nodes: {
		doc: { content: 'block+' },
		paragraph: { group: 'block', content: 'inline*' },
		heading: { group: 'block', content: 'inline*', attrs: { level: { default: 1 } } },
		text: { group: 'inline' },
	},
	marks: { strong: {} },
})
