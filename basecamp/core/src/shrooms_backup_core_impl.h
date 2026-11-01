#pragma once

#include <string>

#include "logos_module_context.h"

/**
 * @brief The Shrooms Backup daemons, for the Shrooms Backup view.
 *
 * A Basecamp view may not open a connection, so this does it: plain HTTP to
 * a backup daemon, on this machine's loopback (port 7395, the address
 * "local") or on a machine's mesh address (port 7394). Any other address is
 * refused before anything is sent: fd00::/8 and 198.18.0.0/15 only.
 *
 * Only the daemons' state and settings pass through here, never a
 * repository: the paths are /v1/info, /v1/runs, /v1/presets, /v1/mirror/...
 * to read, PUT /v1/config and POST /v1/run to write. The daemon decides who
 * may do what; an owner is named by the mesh, so this carries no secret.
 *
 * Calls are short, made on the caller's thread and bounded at a few
 * seconds. Finding the daemons asks every peer at once, each on a thread of
 * its own, and is polled.
 */
class ShroomsBackupCoreImpl : public LogosModuleContext
{
public:
    /**
     * @brief Starts asking this machine and the peers for their daemon's
     * /v1/info, and returns what is known so far:
     * `[{"address","peer","mesh","info":{...}}]`.
     *
     * @param peers "name|mesh|address" entries separated by ";", as the view
     * reads them from shrooms_core's status.
     */
    std::string find(const std::string& peers);

    /** @brief What finding has learned since, the same shape as find(). */
    std::string found();

    /** @brief A GET of a daemon's /v1/ path. The daemon's JSON, or {"error"}. */
    std::string get(const std::string& address, const std::string& path);

    /** @brief PUT /v1/config or POST /v1/run, with a JSON body. The daemon's JSON, or {"error"}. */
    std::string send(const std::string& address, const std::string& method,
                     const std::string& path, const std::string& body);
};
