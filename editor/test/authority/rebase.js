// The writer's rebase, as the Go core must do it (after
// @stepwisehq/prosemirror-collab-commit's applyCommit, MIT): map a commit
// made on version base over the steps committed since, apply what still
// applies, and return the new document and the steps as applied. A step that
// no longer applies is dropped; the commit is still appended (with fewer
// steps, or none), because the editor confirms its commit by ref.
import { Mapping, Transform } from 'prosemirror-transform'

export function applyCommit(doc, since, steps) {
	const newMap = new Mapping(since.map((s) => s.getMap()))
	const mapping = new Mapping(steps.map((s) => s.getMap())).invert()
	mapping.appendMapping(newMap)
	const tr = new Transform(doc)
	for (let i = 0, mapFrom = steps.length; i < steps.length; i++) {
		const mapped = steps[i].map(mapping.slice(mapFrom))
		mapFrom--
		if (mapped && !tr.maybeStep(mapped).failed) {
			mapping.appendMapping(new Mapping(tr.mapping.maps.slice(tr.steps.length - 1)))
			mapping.setMirror(mapFrom, mapping.maps.length - 1)
		}
	}
	return { doc: tr.doc, steps: tr.steps }
}
