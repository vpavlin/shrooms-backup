cmake_minimum_required(VERSION 3.14)
project(ShroomsBackupCorePlugin LANGUAGES CXX)

if(DEFINED ENV{LOGOS_MODULE_BUILDER_ROOT})
    include($ENV{LOGOS_MODULE_BUILDER_ROOT}/cmake/LogosModule.cmake)
elseif(EXISTS "${CMAKE_CURRENT_SOURCE_DIR}/cmake/LogosModule.cmake")
    include(cmake/LogosModule.cmake)
else()
    message(FATAL_ERROR "LogosModule.cmake not found")
endif()

file(READ "${CMAKE_CURRENT_SOURCE_DIR}/metadata.json" METADATA_JSON)
string(JSON MODULE_NAME GET "${METADATA_JSON}" name)

logos_module(
    NAME ${MODULE_NAME}
    SOURCES
        src/shrooms_backup_core_impl.h
        src/shrooms_backup_core_impl.cpp
)
