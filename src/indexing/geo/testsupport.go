package geo

// DefaultTestResolution is the H3 resolution every test fixture across the repo
// builds against, so one changed value updates every fixture together.
//
// NOT a production default: config.Geo.ResolutionCells is what a deployment
// configures, and its own envDefault of 8 agrees with this one only by accident.
const DefaultTestResolution = 8
