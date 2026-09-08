package geo

// DefaultTestResolution is the H3 resolution every test fixture across the
// repo builds against, so one changed value updates every fixture together
// instead of drifting under six different local names (resolution,
// testResolution, indexResolution, res — see the review on PR #18). It is
// not a production default: config.Geo.ResolutionCells is what a deployment
// actually configures, with its own envDefault of 8 that happens to agree
// with this one today for no reason either side depends on.
const DefaultTestResolution = 8
