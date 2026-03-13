# Real-Time WebRTC Audio Routing Server for Voice Agents
This is a server to process RTC connections between browser clients and "server clients", that are spwaned on-demand.

This was built to enable audio processing through a Speech-to-Text.

This repository is heavily inspired by @GRVYDEV's https://github.com/GRVYDEV/S.A.T.U.R.D.A.Y  
It also makes use of @ggerganov's https://github.com/ggerganov/whisper.cpp  


# Setup Instructions  
1. Start the RTC Server -- `cd rtc_server` and `sudo go run rtc_server.go`
2. Fill up the whisper.cpp submodule (which will be empty at first) `git submodule init` and `git submodule update`
3. Build whisper lib to link against: `cd whisper.cpp` and `make libwhisper.a`
4. Fetch the whisper.cpp tiny model (within the whisper.cpp dir) `make tiny.en` and `cp models/ggml-tiny.en.bin ../models/`  
5. Since we'r eimporitng a private Github repository (github.com/infinityp913/rtc-go-server/stt) in this project we need to follow a few steup steps for that:  
    1. `git config --global url."ssh://git@github.com".insteadOf "https://github.com"`
    2. `go env -w GOPRIVATE="github.com/<my_user>/<my_privaterepo>"`
    3. If exists, remove the line `AllowGroups wheel` from the user’s ~/.ssh/config
    4. Set up SSH for Github on the system if you haven’t done so. Follow this: [Setting Up SSH for Github on a system](https://www.notion.so/Setting-Up-SSH-for-Github-on-a-system-0e3717ff0b2c44d2a4fbd12cc13b6a7a?pvs=21) (might need to request access from @Ananth)
6. Run `go mod tidy` in the root level of the repo
7. Run the RTC Client with compiling and linking env variables: cd into the rtc_client dir (important to cd into rtc_client and NOT rtc_client/cmd or any other directory) and run `C_INCLUDE_PATH=${abs path to whisper} LIBRARY_PATH=${abs path to whisper} go run rtc-whisper-client.go`. Like: `C_INCLUDE_PATH=/home/<username>/go/src/rtc-go-server/whisper.cpp LIBRARY_PATH=/home/<username>/go/src/rtc-go-server/whisper.cpp go run cmd/rtc-whisper-client.go`
8. Go to matherium.com/webrtc-demo and click on call button
