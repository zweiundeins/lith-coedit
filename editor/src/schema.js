// schemaSpec turns a schema in the JSON format prosemirror-go reads, with
// "nodes" and "marks" as ordered arrays of specs with their "name", into
// the spec prosemirror-model's Schema takes. extra adds the browser's parts
// per name: {nodes: {image: {toDOM, parseDOM}}, marks: {...}}.
export function schemaSpec(json, extra = {}) {
	const nodes = {}
	for (const { name, ...n } of json.nodes) nodes[name] = { ...n, ...(extra.nodes?.[name] ?? {}) }
	const marks = {}
	for (const { name, ...m } of json.marks ?? []) marks[name] = { ...m, ...(extra.marks?.[name] ?? {}) }
	return { nodes, marks, topNode: json.topNode }
}
