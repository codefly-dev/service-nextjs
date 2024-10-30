export const profileStyles = {
  container: "min-h-screen bg-gray-100 dark:bg-gray-900 py-12 px-4 sm:px-6 lg:px-8",
  card: "max-w-3xl mx-auto bg-white dark:bg-gray-800 shadow-xl rounded-lg overflow-hidden",
  header: "bg-yellow-400 h-32 sm:h-48",
  content: "relative px-4 sm:px-6 lg:px-8 pb-8",
  avatarWrapper: "relative -mt-16 sm:-mt-24",
  avatar: "w-32 h-32 sm:w-48 sm:h-48 rounded-full border-4 border-white dark:border-gray-800 mx-auto",
  nameWrapper: "mt-6 text-center",
  name: "text-3xl font-bold text-gray-900 dark:text-white",
  email: "mt-1 text-gray-600 dark:text-gray-300",
  detailsWrapper: "mt-8 border-t border-gray-200 dark:border-gray-700 pt-8",
  detailsList: "divide-y divide-gray-200 dark:divide-gray-700",
  detailItem: "py-4 sm:py-5 sm:grid sm:grid-cols-3 sm:gap-4",
  detailLabel: "text-sm font-medium text-gray-500 dark:text-gray-400",
  detailValue: "mt-1 text-sm text-gray-900 dark:text-white sm:mt-0 sm:col-span-2",
  buttonWrapper: "mt-8 flex justify-center",
  button: "px-4 py-2 bg-yellow-400 hover:bg-yellow-500 text-gray-900 rounded-md transition duration-300 ease-in-out transform hover:scale-105"
} as const;
