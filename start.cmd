@echo off
rem Start qa-tracker and open the UI.
cd /d "%~dp0"
start "" http://127.0.0.1:7777/
qa.exe serve
