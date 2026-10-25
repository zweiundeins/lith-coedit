package collab

// Cached reports whether the document cache holds a subject's document at
// a version and ref, for tests of what a failed batch leaves.
func Cached(kind, id, epoch string, version int64, ref string) bool {
	_, ok := docs.get(docKey{kind, id, epoch, version, ref})
	return ok
}
