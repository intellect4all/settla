// Package all aggregates blank imports for all provider packages that
// self-register via the factory pattern.
//
// This is the ONLY file that needs editing when adding a new provider.
// Add a blank import for the new provider package and it will be
// automatically discovered at bootstrap time.
package all

import (
	// On-ramp and off-ramp providers.
	_ "github.com/intellect4all/settla/rail/provider/mock"
	_ "github.com/intellect4all/settla/rail/provider/mockhttp"
	_ "github.com/intellect4all/settla/rail/provider/settla"
)
