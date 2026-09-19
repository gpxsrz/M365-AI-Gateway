from __future__ import annotations

import os
import sys
import tempfile
import unittest
from pathlib import Path


class HermesRegistryContractTests(unittest.TestCase):
    def test_real_plugin_manager_exposes_function_definition(self):
        agent_root = os.environ.get("HERMES_AGENT_ROOT")
        plugin_root = os.environ.get("M365_NATIVE_ATTACHMENTS_PLUGIN_ROOT")
        if not agent_root or not plugin_root:
            self.skipTest(
                "set HERMES_AGENT_ROOT and M365_NATIVE_ATTACHMENTS_PLUGIN_ROOT "
                "to run against the real Hermes registry"
            )

        sys.path.insert(0, agent_root)
        from hermes_cli.plugins import PluginManager, _plugin_home_scope
        from hermes_cli.plugins_manifest import PluginManifest
        from tools.registry import registry

        manifest = PluginManifest(
            name="m365-native-attachments",
            version="1.0.0",
            description="isolated native attachment registry contract probe",
            source="user",
            path=plugin_root,
            key="m365-native-attachments",
        )

        with tempfile.TemporaryDirectory(prefix="m365-registry-contract-") as home:
            manager = PluginManager(scope_key=home)
            manager._load_plugin(manifest)
            with _plugin_home_scope(Path(home)):
                definitions = registry.get_definitions(
                    {"m365_native_attach"}, quiet=True
                )
                entry = registry.get_entry(
                    "m365_native_attach", scope=home
                )

            self.assertIsNotNone(entry)
            self.assertEqual(len(definitions), 1)
            function = definitions[0]["function"]
            self.assertEqual(
                set(function), {"name", "description", "parameters"}
            )
            self.assertEqual(function["name"], "m365_native_attach")
            self.assertTrue(function["description"])

            parameters = function["parameters"]
            self.assertEqual(parameters["type"], "object")
            self.assertFalse(parameters["additionalProperties"])
            self.assertEqual(set(parameters["properties"]), {"files"})
            self.assertIn("files", parameters["required"])
            files = parameters["properties"]["files"]
            self.assertEqual(files["type"], "array")
            self.assertEqual((files["minItems"], files["maxItems"]), (1, 2))
            item = files["items"]
            self.assertEqual(item["type"], "object")
            self.assertFalse(item["additionalProperties"])
            self.assertEqual(item["required"], ["local_path"])
            self.assertEqual(
                set(item["properties"]),
                {
                    "local_path",
                    "attachment_id",
                    "source_message_id",
                    "expected_sha256",
                },
            )


if __name__ == "__main__":
    unittest.main()
