@echo off
setlocal

echo ======================================
echo Build Go Linux Binaries
echo ======================================

:: 编译目标
set GOOS=linux
set GOARCH=amd64
set CGO_ENABLED=0

:: 输出目录
if not exist bin (
    mkdir bin
)

echo.
echo Building api...
go build -trimpath -ldflags="-s -w" -o bin/api ./control/api
if errorlevel 1 goto :error

echo Building scanner...
go build -trimpath -ldflags="-s -w" -o bin/scanner ./control/scanner
if errorlevel 1 goto :error

echo Building sign...
go build -trimpath -ldflags="-s -w" -o bin/sign ./control/sign
if errorlevel 1 goto :error

echo Building sweep...
go build -trimpath -ldflags="-s -w" -o bin/sweep ./control/sweep
if errorlevel 1 goto :error

echo Building withdraw...
go build -trimpath -ldflags="-s -w" -o bin/withdraw ./control/withdraw
if errorlevel 1 goto :error

echo.
echo ======================================
echo Build Success!
echo ======================================
dir bin

endlocal
exit /b 0

:error
echo.
echo ======================================
echo Build Failed!
echo ======================================
pause
endlocal
exit /b 1