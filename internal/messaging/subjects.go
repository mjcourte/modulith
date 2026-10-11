package messaging

// TODO expand here, ensure we have the correct wildcards
// for userID and projectID
const (
	// document transport: deltas + snapshots for WAL
	// this is an all-replicas/peers transport layer
	// delta are to be edit/upserted into local replica
	// snapshots are to be merged into local replica
	SubjectDocumentDelta    = "document.{projectID}.delta"
	SubjectDocumentSnapshot = "document.{projectID}.snapshot"

	// UserState subjects -> cursors and awareness
	// this is a FE multiplayer/P2P feature.
	// want what users are currently online, and their mouse cursor xy.
	// the details of which live in FE + gateway,
	// but this is the transport stream subject
	SubjectUserStateIsViewing      = "userstate.{projectID}.{userID}.viewing"
	SubjectUserStateCursorPosition = "userstate.{projectID}.{userID}.cursorpos"

	// TODO - the google docs CRDT layers inside the canvas
	// this is a FE multiplayer/P2P feature (unless agent MCP text editing?)
	// SubjectDocumentTextNode<> = "document.{projectID}.{nodeID}.<>"
	// similar, awareness + text cursor position
	// the data exchange can transport over documentdelta
	// Never got this far
)
