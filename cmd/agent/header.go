package main

// header is shown above the deployment progress screen. Keep it within the
// console width the agent logs at startup ("console=COLSxROWS").
var header = []string{
	`▄▄▄▄▄▄▄   ▄▄▄   ▄▄▄  ▄▄▄▄▄▄▄       ▄▄▄    ▄▄▄                ▄▄             ▄▄             `,
	`███▀▀███▄ ████▄████ ███▀▀▀▀▀       ████▄  ███        ██      ██             ██             `,
	`███▄▄███▀  ▀█████▀  ███▄▄          ███▀██▄███ ▄█▀█▄ ▀██▀▀ ▄████ ▄█▀█▄ ████▄ ██ ▄███▄ ██ ██ `,
	`███▀▀▀▀   ▄███████▄ ███      ▀▀▀▀▀ ███  ▀████ ██▄█▀  ██   ██ ██ ██▄█▀ ██ ██ ██ ██ ██ ██▄██ `,
	`███       ███▀ ▀███ ▀███████       ███    ███ ▀█▄▄▄  ██   ▀████ ▀█▄▄▄ ████▀ ██ ▀███▀  ▀██▀ `,
	`                                                                      ██               ██  `,
	`                                                                      ▀▀             ▀▀▀   `,
}
