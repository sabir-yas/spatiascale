package spatial

import "math"

// Point is a 2D geographic coordinate with a unique ID and optional metadata.
// X = longitude (-180 to 180), Y = latitude (-90 to 90).
// Payload is kept small — think a category tag or a sensor reading.
type Point struct {
	ID      uint64
	X, Y    float64
	Payload []byte
}

// BoundingBox is an axis-aligned rectangle that defines a region of space.
// Used for quadtree node boundaries and range query inputs.
type BoundingBox struct {
	MinX, MinY float64
	MaxX, MaxY float64
}

// Contains returns true if the point falls within or on the boundary of this box.
func (b BoundingBox) Contains(p Point) bool {
	return p.X >= b.MinX && p.X <= b.MaxX &&
		p.Y >= b.MinY && p.Y <= b.MaxY
}

// Intersects returns true if two bounding boxes overlap (including edge touches).
func (b BoundingBox) Intersects(other BoundingBox) bool {
	return b.MinX <= other.MaxX && b.MaxX >= other.MinX &&
		b.MinY <= other.MaxY && b.MaxY >= other.MinY
}

// Subdivide splits the box into four equal quadrants: NE, NW, SE, SW.
// This is the core operation of quadtree splitting.
func (b BoundingBox) Subdivide() [4]BoundingBox {
	midX := (b.MinX + b.MaxX) / 2
	midY := (b.MinY + b.MaxY) / 2
	return [4]BoundingBox{
		{midX, midY, b.MaxX, b.MaxY}, // NE
		{b.MinX, midY, midX, b.MaxY}, // NW
		{midX, b.MinY, b.MaxX, midY}, // SE
		{b.MinX, b.MinY, midX, midY}, // SW
	}
}

// Center returns the midpoint of the bounding box.
func (b BoundingBox) Center() Point {
	return Point{
		X: (b.MinX + b.MaxX) / 2,
		Y: (b.MinY + b.MaxY) / 2,
	}
}

// Area returns the area of the bounding box.
func (b BoundingBox) Area() float64 {
	return (b.MaxX - b.MinX) * (b.MaxY - b.MinY)
}

// DistanceTo returns the Euclidean distance between two points.
// For real geospatial use you'd use the Haversine formula, but Euclidean
// is sufficient for our spatial indexing and benchmark purposes.
func (p Point) DistanceTo(other Point) float64 {
	dx := p.X - other.X
	dy := p.Y - other.Y
	return math.Sqrt(dx*dx + dy*dy)
}

// WorldBounds is the bounding box covering all valid WGS84 coordinates.
var WorldBounds = BoundingBox{
	MinX: -180, MinY: -90,
	MaxX: 180, MaxY: 90,
}
