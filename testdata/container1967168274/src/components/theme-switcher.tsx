"use client";

import { MoonIcon, SunIcon } from "@radix-ui/react-icons";
import { useTheme } from "next-themes";

export const ThemeSwitcher = () => {
  const { systemTheme, theme, setTheme } = useTheme();
  const currentTheme = theme === "system" ? systemTheme : theme;

  return currentTheme === "dark" ? (
    <SunIcon
      className="w-4 h-4 text-neutral-300"
      role="button"
      onClick={() => setTheme("light")}
    />
  ) : (
    <MoonIcon
      className="w-4 h-4 text-gray-900"
      role="button"
      onClick={() => setTheme("dark")}
    />
  );
};
