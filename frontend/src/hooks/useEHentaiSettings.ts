"use client";

import { useCallback, useEffect, useState } from "react";
import { apiPath } from "@/lib/base-path";

export interface EHentaiSettings {
  enabled: boolean;
  site: "ehentai" | "exhentai";
  preferOriginalTitle: boolean;
  searchExpunged: boolean;
  forcedLanguage: string;
  credentialsConfigured: boolean;
  configurationValid: boolean;
  configurationError?: string;
}

export function useEHentaiSettings() {
  const [settings, setSettings] = useState<EHentaiSettings | null>(null);

  const refresh = useCallback(async () => {
    try {
      const response = await fetch(apiPath("/api/metadata/ehentai/settings"), {
        credentials: "include",
      });
      if (!response.ok) {
        setSettings(null);
        return;
      }
      setSettings(await response.json());
    } catch {
      setSettings(null);
    }
  }, []);

  useEffect(() => {
    void refresh();
  }, [refresh]);

  return { settings, refresh };
}
