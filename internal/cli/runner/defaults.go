package runner

// Each of these is two things at once: the value a flag defaults to, and the
// sentinel meaning "the caller did not choose a path", which generate/surface
// compare against before yielding to scorix.yaml. A second copy of either
// literal is therefore a trap - change one and the comparison stops matching in
// silence, leaving the manifest ignored.
const (
	DefaultProtoPath  = "idl/app.proto"
	DefaultSchemaPath = "idl/schema.sql"
)
