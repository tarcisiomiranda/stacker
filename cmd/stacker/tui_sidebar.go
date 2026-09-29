package main

func sidebarVisibleRows(height, total int) int {
	if height <= 0 {
		return max(1, total)
	}
	return max(1, height-5)
}

func sidebarSelectionOffset(offset, selected, total, visible int) int {
	maximum := max(0, total-visible)
	offset = clamp(offset, 0, maximum)
	if selected < offset {
		offset = selected
	} else if selected >= offset+visible {
		offset = selected - visible + 1
	}
	return clamp(offset, 0, maximum)
}

func sidebarScrollOffset(offset, total, visible, delta int) int {
	return clamp(offset+delta, 0, max(0, total-visible))
}
