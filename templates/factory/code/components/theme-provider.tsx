import { ThemeProvider as NextThemesThemeProvider } from "next-themes";
import { useState, useEffect } from "react";

type Props = {
  children: string | React.JSX.Element | React.JSX.Element[];
};

export const ThemeProvider = ({ children }: Props) => {
  const [mounted, setMounted] = useState<boolean>(false);

  useEffect(() => {
    setMounted(true);
  }, []);

  if (!mounted) {
    return <>{children}</>;
  }

  return (
    <NextThemesThemeProvider enableSystem={true} attribute="class">
      {children}
    </NextThemesThemeProvider>
  );
};
