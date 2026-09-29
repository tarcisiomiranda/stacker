package main

type paneRect struct {
	X, Y, Width, Height int
}

type splitOrientation string

const (
	stackedOrientation    splitOrientation = "stacked"
	sideBySideOrientation splitOrientation = "side-by-side"
)

func clampSidebarWidth(screenWidth, preferred, fallback int) int {
	if screenWidth <= 0 {
		return max(16, fallback)
	}
	if preferred <= 0 {
		preferred = fallback
	}
	maximum := max(16, min(screenWidth/2, screenWidth-23))
	return clamp(preferred, 16, maximum)
}

func logPaneRects(screenWidth, screenHeight, sidebarWidth int, requested splitOrientation) (paneRect, paneRect, splitOrientation, bool) {
	width := max(20, screenWidth-sidebarWidth-1)
	height := max(5, screenHeight-2)
	primary := paneRect{X: sidebarWidth, Y: 0, Width: width, Height: height}
	if requested == sideBySideOrientation && width >= 57 {
		primary.Width = (width - 1) / 2
		secondary := paneRect{X: primary.X + primary.Width - 1, Y: 0, Width: width - 1 - primary.Width, Height: height}
		return primary, secondary, sideBySideOrientation, true
	}
	if height >= 12 {
		primary.Height = (height - 1) / 2
		secondary := paneRect{X: primary.X, Y: primary.Y + primary.Height, Width: width, Height: height - 1 - primary.Height}
		return primary, secondary, stackedOrientation, true
	}
	return primary, paneRect{}, stackedOrientation, false
}
