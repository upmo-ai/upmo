package app

// CanaryQuestions is the synthetic memory set. Each expected claim is in
// CanaryClaims. The extra claims keep a dump of the whole set from being
// the only way to score a hit.
func CanaryQuestions() []Question {
	return []Question{
		{Query: "which tea does Ada prefer", Expect: "Ada prefers oolong tea"},
		{Query: "where does Lee work", Expect: "Lee works in Oslo"},
		{Query: "where is the office kettle", Expect: "The office kettle is on the second floor"},
	}
}

// CanaryClaims is the stored text for that set, plus neighbors that no
// question asks for.
func CanaryClaims() []string {
	return []string{
		"Ada prefers oolong tea",
		"Lee works in Oslo",
		"The office kettle is on the second floor",
		"The printer sits by the stairs",
		"The blue notebook stays in the cupboard",
		"Spare chairs are stacked in the hall",
		"Monday deliveries arrive at the side door",
		"The yellow lamp needs a new bulb",
		"Raincoats hang beside the lockers",
		"The cactus sits on the windowsill",
		"Empty boxes are flattened for recycling",
		"The red mug belongs in the dishwasher",
		"Sticky notes cover the glass partition",
		"The spare key is inside the tin",
		"Sketch paper is stored under the desk",
		"The wall clock gains a minute each week",
	}
}
