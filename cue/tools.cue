package config

#Tool: {
	name:        string
	description: string
	dangerous:   bool
}

tools: [
	{
		name:        "shell"
		description: "Execute shell commands"
		dangerous:   true
	},
	{
		name:        "read_file"
		description: "Read files"
		dangerous:   false
	},
]
