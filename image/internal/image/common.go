package image

// commonImage shares code implementing some parts of types.Image
// for implementations of genericManifest.
type commonImage struct {
	genericManifest // Embedded anonymously, so that an anonymous embedding of commonImage exposes the genericManifest methods as well.
}
