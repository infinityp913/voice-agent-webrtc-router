# RTC Go Server
This is a server to process RTC connections between browser clients and "server clients" and to process the audio through a Speech-to-Text.   
This repository is heavily inspired by @GRVYDEV's https://github.com/GRVYDEV/S.A.T.U.R.D.A.Y  
It also makes use of @ggerganov's https://github.com/ggerganov/whisper.cpp  


# Setup Instructions  
1. `git submodule init` and `git submodule update` - to fill up the whisper.cpp submodule (which will be empty at first)
2. `cd whisper.cpp` and `make libwhisper.a` to build whisper lib to link against
3. (within the whisper.cpp dir) `make tiny.en` and `cp models/ggml-tiny.en.bin ../models/` - to fetch the whisper.cpp tiny model
4. cd into the rtc-client dir and run `C_INCLUDE_PATH=${abs path to whisper} LIBRARY_PATH=${abs path to whisper} go run rtc-whisper-client.go` to run the rtc-client with compiling and linking env variables
