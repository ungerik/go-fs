package s3fs

// Region represents an AWS region as an integer. The Region constants can be
// used to select a region and can then be converted into their respective
// string representations.
type Region int

const (
	// USEast1 (N. Virginia)
	USEast1 Region = iota
	// USEast2 (Ohio)
	USEast2
	// USWest1 (N. California)
	USWest1
	// USWest2 (Oregon)
	USWest2
	// CACentral1 (Central)
	CACentral1
	// APSouth1 (Mumbai)
	APSouth1
	// APNorthEast1 (Tokyo)
	APNorthEast1
	// APNorthEast2 (Seoul)
	APNorthEast2
	// APNorthEast3 (Osaka-Local)
	APNorthEast3
	// APSouthEast1 (Singapore)
	APSouthEast1
	// APSouthEast2 (Sydney)
	APSouthEast2
	// CNNorth1 (Beijing)
	CNNorth1
	// CNNorthWest1 (Ningxia)
	CNNorthWest1
	// EUCentral1 (Frankfurt)
	EUCentral1
	// EUWest1 (Ireland)
	EUWest1
	// EUWest2 (London)
	EUWest2
	// EUWest3 (Paris)
	EUWest3
	// SAEast1 (São Paulo)
	SAEast1
)

func (r Region) String() string {
	return [...]string{
		"us-east-1",
		"us-east-2",
		"us-west-1",
		"us-west-2",
		"ca-central-1",
		"ap-south-1",
		"ap-northeast-1",
		"ap-northeast-2",
		"ap-northeast-3",
		"ap-southeast-1",
		"ap-southeast-2",
		"cn-north-1",
		"cn-northwest-1",
		"eu-central-1",
		"eu-west-1",
		"eu-west-2",
		"eu-west-3",
		"sa-east-1",
	}[r]
}
